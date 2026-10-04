// Package contacts 实现通信录（REQ-UI-032 ~ REQ-UI-037）的本地 SQLite 存储。
//
// 骨架照 internal/calllog（单 worker goroutine 串行执行全部 SQL、submit 同步
// 入队、Close 幂等），但**没有 post 异步入队**：通信录的写入全部来自界面与 WS
// （没有任何信令线程路径），同步执行即可，还能顺便保证"写完立刻重建内存快照"
// 的一致性。
//
// 三条设计约束：
//
//  1. **主键是规范化号码**（core.ContactKey）：同一个号码的不同写法在库里是同
//     一条记录，去重与匹配都靠它。
//  2. **重名判断与写入在同一事务**。新增/改号时若新号码已被别的条目占用，
//     返回 core.ErrContactNumberExists 且整条不落库——不能"先查后写"，
//     两个并发请求会同时通过检查。
//  3. **导入时常用标志"只增不减"**（Merge）：已存在的条目只覆盖号码/名称/备注，
//     常用标志取"本地 or 导入"——本地已标常用就保持，本地没标而导入标了就跟着
//     变常用（用户口径：导入不会把用户标的常用冲掉，但能把文件里的常用带进来）。
package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO，交叉编译不受影响）

	"mti-sip-phone/internal/core"
	"mti-sip-phone/internal/logx"
)

// defaultQueueSize SQL 任务队列容量（界面/WS 路径，几乎不可能堆满）。
const defaultQueueSize = 64

// deleteChunk 批量删除时每批的占位符个数（避开 SQLite 的变量个数上限）。
const deleteChunk = 400

const schemaSQL = `
CREATE TABLE IF NOT EXISTS contact (
  key        TEXT PRIMARY KEY,
  number     TEXT NOT NULL,
  name       TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  starred    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
PRAGMA user_version = 1;
`

// errClosed 存储已关闭（退出过程中的查询）。
var errClosed = errors.New("通信录已关闭")

// Store 通信录存储。零值不可用，用 Open 创建。
type Store struct {
	db  *sql.DB
	ops chan func(*sql.DB) error

	mu      sync.Mutex // 护 closing 与向 ops 发送（Close 与入队互斥）
	closing bool

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// Open 打开（必要时创建）通信录数据库并启动 worker。
// 打开失败由调用方降级处理（本次运行没有通信录），不应阻断程序启动。
func Open(path string) (*Store, error) {
	// DSN 只放 busy_timeout（失败不影响建连接）；journal_mode=WAL 放到打开之后
	// 单独执行并容忍失败（网络盘/同步盘上可能建不起来），与 calllog 一致。
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开通信录数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1) // 单连接串行，杜绝写锁竞争
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接通信录数据库失败: %w", err)
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(pragma); err != nil {
			logx.Logger().Warn("通信录数据库 PRAGMA 未生效（已忽略）", "pragma", pragma, "err", err)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化通信录表失败: %w", err)
	}

	s := &Store{db: db, ops: make(chan func(*sql.DB) error, defaultQueueSize)}
	s.wg.Add(1)
	go s.worker()
	return s, nil
}

// worker 串行执行所有 SQL。
func (s *Store) worker() {
	defer s.wg.Done()
	for fn := range s.ops {
		if err := fn(s.db); err != nil {
			logx.Logger().Warn("通信录数据库操作失败", "err", err)
		}
	}
}

// submit 入队并等结果（全部调用路径），返回业务错误。
func (s *Store) submit(fn func(*sql.DB) error) error {
	done := make(chan error, 1)
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return errClosed
	}
	s.ops <- func(db *sql.DB) error {
		done <- fn(db)
		return nil
	}
	s.mu.Unlock()
	return <-done
}

// Close 关闭存储：等队列排空后关闭数据库。可重复调用（幂等）。
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		close(s.ops)
		s.mu.Unlock()
		s.wg.Wait()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// ---------------------------------------------------------------------------
// 读写
// ---------------------------------------------------------------------------

// List 全量快照（按规范化号码排序，顺序稳定）。
func (s *Store) List() ([]core.Contact, error) {
	var out []core.Contact
	err := s.submit(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT number, name, note, starred, updated_at
			FROM contact ORDER BY key`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			var (
				c         core.Contact
				starred   int
				updatedAt int64
			)
			if err := rows.Scan(&c.Number, &c.Name, &c.Note, &starred, &updatedAt); err != nil {
				return err
			}
			c.Starred = starred != 0
			c.UpdatedAt = fromMillis(updatedAt)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// Save 保存联系人：prevNumber 为空 = 新增；非空 = 修改（号码可改，传改前的号码）。
// 新号码若已被其它条目占用，返回 core.ErrContactNumberExists 且不落库。
func (s *Store) Save(prevNumber string, c core.Contact) error {
	key := core.ContactKey(c.Number)
	if key == "" {
		return core.ErrContactNumberEmpty
	}
	prevKey := core.ContactKey(prevNumber)
	return s.submit(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		// 新增、或改成了别的号码：该号码必须还没有条目
		if prevKey != key {
			taken, err := existsTx(tx, key)
			if err != nil {
				return err
			}
			if taken {
				return core.ErrContactNumberExists
			}
		}
		if prevKey != "" && prevKey != key {
			if _, err := tx.Exec(`DELETE FROM contact WHERE key = ?`, prevKey); err != nil {
				return err
			}
		}
		now := time.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO contact
			(key, number, name, note, starred, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				number = excluded.number,
				name   = excluded.name,
				note   = excluded.note,
				starred = excluded.starred,
				updated_at = excluded.updated_at`,
			key, c.Number, c.Name, c.Note, boolToInt(c.Starred), now, now); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// SetStarred 批量设置/取消常用标志，返回受影响条数。
func (s *Store) SetStarred(numbers []string, starred bool) (int, error) {
	if len(numbers) == 0 {
		return 0, nil
	}
	total := 0
	err := s.submit(func(db *sql.DB) error {
		total = 0
		for _, chunk := range chunks(numbers) {
			res, err := db.Exec(`UPDATE contact SET starred = ?, updated_at = ?
				WHERE key IN (`+placeholders(len(chunk))+`)`,
				append([]any{boolToInt(starred), time.Now().UnixMilli()}, toArgs(chunk)...)...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				total += int(n)
			}
		}
		return nil
	})
	return total, err
}

// Delete 按规范化号码删除若干条，返回实际删除条数。
func (s *Store) Delete(numbers []string) (int, error) {
	if len(numbers) == 0 {
		return 0, nil
	}
	total := 0
	err := s.submit(func(db *sql.DB) error {
		total = 0
		for _, chunk := range chunks(numbers) {
			res, err := db.Exec(`DELETE FROM contact WHERE key IN (`+placeholders(len(chunk))+`)`,
				toArgs(chunk)...)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				total += int(n)
			}
		}
		return nil
	})
	return total, err
}

// Clear 清空通信录，返回删除条数。
func (s *Store) Clear() (int, error) {
	total := 0
	err := s.submit(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM contact`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total = int(n)
		return nil
	})
	return total, err
}

// Merge 按号码合并导入（单事务）：已存在的条目**只覆盖号码/名称/备注，保留常用
// 标志**；不存在的整条新增（常用标志取导入值）。返回新增与覆盖条数。
func (s *Store) Merge(list []core.Contact) (added, updated int, err error) {
	if len(list) == 0 {
		return 0, 0, nil
	}
	err = s.submit(func(db *sql.DB) error {
		added, updated = 0, 0
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		batch := make(map[string]bool, len(list)) // 本批已处理过的号码
		now := time.Now().UnixMilli()
		for _, c := range list {
			key := core.ContactKey(c.Number)
			if key == "" {
				continue
			}
			known := batch[key]
			if !known {
				taken, err := existsTx(tx, key)
				if err != nil {
					return err
				}
				if taken {
					updated++
				} else {
					added++
				}
				batch[key] = true
			} else {
				updated++ // 同一文件里的重复号码：按"再覆盖一次"计
			}
			// 新增行采用导入的常用标志；已存在的行 **starred 取本地与导入的"或"**：
			// 导入标了常用就变成常用，没标则保留本地原值（只增不减）
			if _, err := tx.Exec(`INSERT INTO contact
				(key, number, name, note, starred, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(key) DO UPDATE SET
					number = excluded.number,
					name   = excluded.name,
					note   = excluded.note,
					starred = MAX(contact.starred, excluded.starred),
					updated_at = excluded.updated_at`,
				key, c.Number, c.Name, c.Note, boolToInt(c.Starred), now, now); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return 0, 0, err
	}
	return added, updated, nil
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// existsTx 事务内判断主键是否存在。
func existsTx(tx *sql.Tx, key string) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM contact WHERE key = ?`, key).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// chunks 把列表切成每批 deleteChunk 个。
func chunks(list []string) [][]string {
	var out [][]string
	for start := 0; start < len(list); start += deleteChunk {
		end := start + deleteChunk
		if end > len(list) {
			end = len(list)
		}
		out = append(out, list[start:end])
	}
	return out
}

func toArgs(list []string) []any {
	args := make([]any, len(list))
	for i, s := range list {
		args[i] = s
	}
	return args
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return "?" + strings.Repeat(",?", n-1)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fromMillis Unix 毫秒 → time.Time，0 还原为零值。
func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
