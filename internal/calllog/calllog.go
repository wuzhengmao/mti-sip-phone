// Package calllog 实现通话记录（REQ-CALL-007）的本地 SQLite 存储。
//
// 三条设计约束（改动前先读，别"顺手简化"）：
//
//  1. **所有 SQL 都在单个 worker goroutine 上串行执行**。SIP 线程的写入
//     （Start/MarkAnswered/Finish）只做一次 channel send、永不阻塞信令；
//     界面的查询走同一条 FIFO 队列，因此必然排在已入队的写入之后
//     （读己之写）——这正是"通话结束事件到达界面后立刻查库能看到新记录"
//     能成立的前提。用 `go fn()` 逐条并发是错的：Finish 可能抢在 Start
//     之前执行，UPDATE 影响 0 行。
//  2. **幂等**。call_id 是主键，Start 用 INSERT OR IGNORE，Finish 带
//     `ended_at = 0` 条件：SIP 层对未接通的呼出会上报两次结束
//     （OnFailed 之后 defer 还会再报一次 OnTerminated，见 sip/phone.go），
//     只有第一次生效——保留第一次更具体的原因（如 "486 Busy Here"）。
//  3. **时间戳用 Unix 毫秒存，0 表示"未接通/进行中"**。与 time.Time 零值的
//     互转集中在 toMillis/fromMillis，别让 time.UnixMilli(0)（1970 年）
//     漏到界面。
package calllog

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

// DefaultPageSize 查询默认每页条数（界面滚动分页每页拉这么多）。
const DefaultPageSize = 50

const (
	// maxTextLen 号码/显示名/原因的最大长度（按 rune）：这些字段来自
	// SIP 报文，属不可信输入，落库前截断。
	maxTextLen = 256
	// writeQueueSize 写入队列容量。单通话应用几乎不可能堆满；真满了就
	// 丢弃并告警（与事件总线"满即丢"同一哲学），绝不阻塞信令线程。
	writeQueueSize = 256
	// deleteChunk 按 call_id 删除时每批的占位符个数（避开 SQLite 的
	// 变量个数上限）。
	deleteChunk = 400
)

// schemaSQL 建表语句。call_id 直接作主键（天然唯一，界面用它做选中/删除，
// 不需要再引入自增 id）；等时间戳排序用 rowid 兜底保证顺序稳定。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS call_log (
  call_id     TEXT PRIMARY KEY,
  direction   TEXT    NOT NULL,
  remote      TEXT    NOT NULL,
  remote_name TEXT    NOT NULL DEFAULT '',
  display_name TEXT   NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL,
  answered_at INTEGER NOT NULL DEFAULT 0,
  ended_at    INTEGER NOT NULL DEFAULT 0,
  has_video   INTEGER NOT NULL DEFAULT 0,
  missed      INTEGER NOT NULL DEFAULT 0,
  reason      TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_call_log_started ON call_log(started_at DESC);
PRAGMA user_version = 2;
`

// errClosed 存储已关闭（退出过程中的查询）。
var errClosed = errors.New("通话记录已关闭")

// Store 通话记录存储。零值不可用，用 Open 创建。
type Store struct {
	db  *sql.DB
	ops chan func(*sql.DB) error

	mu      sync.Mutex // 护 closing 与向 ops 发送（Close 与入队互斥）
	closing bool

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// Open 打开（必要时创建）通话记录数据库并启动 worker。
// 打开失败由调用方降级处理（不记录通话），不应阻断程序启动。
func Open(path string) (*Store, error) {
	// DSN 只放 busy_timeout：它失败不影响建连接。journal_mode=WAL 一旦
	// 失败会让连接直接建不起来（网络盘/同步盘常见），所以放到打开之后
	// 单独执行并容忍失败。
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开通话记录数据库失败: %w", err)
	}
	// 低流量本地库：单连接串行最省心，也杜绝写锁竞争导致的 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接通话记录数据库失败: %w", err)
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(pragma); err != nil {
			logx.Logger().Warn("通话记录数据库 PRAGMA 未生效（已忽略）", "pragma", pragma, "err", err)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化通话记录表失败: %w", err)
	}
	// 老库补列（user_version=1 没有 display_name）：CREATE TABLE IF NOT EXISTS
	// 不会改已存在的表，必须显式检查后 ALTER。
	if err := ensureCallLogColumns(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("升级通话记录表失败: %w", err)
	}

	s := &Store{db: db, ops: make(chan func(*sql.DB) error, writeQueueSize)}
	// 启动清扫必须在 worker 起来之前做完（此刻还没有本次运行的记录）
	s.sweepInterrupted()
	s.wg.Add(1)
	go s.worker()
	return s, nil
}

// ensureCallLogColumns 给旧版本建出来的表补新增列（当前需要 display_name）。
func ensureCallLogColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(call_log)`)
	if err != nil {
		return err
	}
	has := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "display_name" {
			has = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE call_log ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	return err
}

// sweepInterrupted 把上次运行遗留的"进行中"记录标为中断：进程崩溃或
// 自更新重启时不会有结束上报，不清扫就会永远显示"进行中"。
// MAX(started_at, answered_at) 保证结束时刻不早于开始/接通时刻（时长非负）。
func (s *Store) sweepInterrupted() {
	res, err := s.db.Exec(`
		UPDATE call_log SET
			ended_at = MAX(started_at, answered_at),
			reason   = ?,
			missed   = CASE WHEN direction = 'incoming' AND answered_at = 0 THEN 1 ELSE 0 END
		WHERE ended_at = 0`, core.ReasonInterrupted)
	if err != nil {
		logx.Logger().Warn("清扫上次未结束的通话记录失败", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logx.Logger().Info("已把上次运行遗留的通话记录标记为中断", "count", n)
	}
}

// worker 串行执行所有 SQL（见包注释的约束 1）。
func (s *Store) worker() {
	defer s.wg.Done()
	for fn := range s.ops {
		if err := fn(s.db); err != nil {
			logx.Logger().Warn("通话记录数据库操作失败", "err", err)
		}
	}
}

// post 异步入队（SIP 线程路径，绝不阻塞）。队列满则丢弃并告警。
func (s *Store) post(fn func(*sql.DB) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	select {
	case s.ops <- fn:
	default:
		logx.Logger().Warn("通话记录写入队列已满，本次写入被丢弃")
	}
}

// submit 同步入队并等结果（界面路径）。
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
// 写入（SIP/核心线程调用，非阻塞）
// ---------------------------------------------------------------------------

// Start 记一次通话的开始。同一 call_id 重复调用无副作用。
func (s *Store) Start(r core.CallRecord) {
	s.post(func(db *sql.DB) error {
		_, err := db.Exec(`INSERT OR IGNORE INTO call_log
			(call_id, direction, remote, remote_name, display_name, started_at, answered_at,
			 ended_at, has_video, missed, reason, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			truncate(r.CallID), truncate(r.Direction), truncate(r.Remote), truncate(r.RemoteName),
			truncate(r.DisplayName),
			toMillis(r.StartedAt), toMillis(r.AnsweredAt), toMillis(r.EndedAt),
			boolToInt(r.HasVideo), boolToInt(r.Missed), truncate(r.Reason),
			time.Now().UnixMilli())
		return err
	})
}

// MarkAnswered 记接通时刻（首次生效）。
func (s *Store) MarkAnswered(callID string, at time.Time) {
	s.post(func(db *sql.DB) error {
		_, err := db.Exec(
			`UPDATE call_log SET answered_at = ? WHERE call_id = ? AND answered_at = 0`,
			toMillis(at), callID)
		return err
	})
}

// Finish 记结束：结束时刻、原因与是否未接。只有第一次生效——重复上报
// （见包注释）不会覆盖第一次的原因与时长。
func (s *Store) Finish(callID string, at time.Time, reason string, missed bool) {
	if at.IsZero() {
		at = time.Now() // 结束时刻不能是 0（0 在库里表示"进行中"）
	}
	s.post(func(db *sql.DB) error {
		res, err := db.Exec(
			`UPDATE call_log SET ended_at = ?, reason = ?, missed = ?
			 WHERE call_id = ? AND ended_at = 0`,
			toMillis(at), truncate(reason), boolToInt(missed), callID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			logx.Logger().Debug("通话记录结束更新未命中（重复上报或开始记录缺失）", "callId", callID)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 查询与删除（界面线程调用，同步）
// ---------------------------------------------------------------------------

// Query 分页查询通话记录（按开始时间倒序）。f.Remote 非空时按号码子串筛选。
func (s *Store) Query(f core.CallLogFilter) (core.CallLogPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	var page core.CallLogPage
	err := s.submit(func(db *sql.DB) error {
		where, args := filterSQL(f.Remote)
		if err := db.QueryRow(`SELECT COUNT(*) FROM call_log`+where, args...).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := db.Query(`SELECT call_id, direction, remote, remote_name, display_name,
			started_at, answered_at, ended_at, has_video, missed, reason
			FROM call_log`+where+` ORDER BY started_at DESC, rowid DESC LIMIT ? OFFSET ?`,
			append(args, limit+1, f.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()

		records := make([]core.CallRecord, 0, limit)
		for rows.Next() {
			var (
				r                        core.CallRecord
				started, answered, ended int64
				hasVideo, missed         int
			)
			if err := rows.Scan(&r.CallID, &r.Direction, &r.Remote, &r.RemoteName, &r.DisplayName,
				&started, &answered, &ended, &hasVideo, &missed, &r.Reason); err != nil {
				return err
			}
			r.StartedAt, r.AnsweredAt, r.EndedAt = fromMillis(started), fromMillis(answered), fromMillis(ended)
			r.HasVideo, r.Missed = hasVideo != 0, missed != 0
			records = append(records, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// 多取一条只用于判断还有没有下一页，不返回给调用方
		if len(records) > limit {
			page.HasMore = true
			records = records[:limit]
		}
		page.Records = records
		return nil
	})
	if err != nil {
		return core.CallLogPage{}, err
	}
	return page, nil
}

// Delete 按 call_id 删除若干条，返回实际删除条数。
func (s *Store) Delete(callIDs []string) (int, error) {
	if len(callIDs) == 0 {
		return 0, nil
	}
	total := 0
	err := s.submit(func(db *sql.DB) error {
		total = 0 // 重试时不要累加
		for start := 0; start < len(callIDs); start += deleteChunk {
			end := start + deleteChunk
			if end > len(callIDs) {
				end = len(callIDs)
			}
			chunk := callIDs[start:end]
			args := make([]any, len(chunk))
			for i, id := range chunk {
				args[i] = id
			}
			placeholders := "?" + strings.Repeat(",?", len(chunk)-1)
			res, err := db.Exec(`DELETE FROM call_log WHERE call_id IN (`+placeholders+`)`, args...)
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

// Clear 清空全部通话记录，返回删除条数。
func (s *Store) Clear() (int, error) {
	total := 0
	err := s.submit(func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM call_log`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		total = int(n)
		return nil
	})
	return total, err
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// filterSQL 组装筛选条件。用 instr 而不是 LIKE：用户在筛选框里输入的
// % 与 _ 必须当普通字符（LIKE 下它们是通配符，会意外匹配全部）。
func filterSQL(remote string) (string, []any) {
	if remote == "" {
		return "", nil
	}
	return ` WHERE instr(remote, ?) > 0`, []any{remote}
}

// toMillis time.Time → Unix 毫秒，零值写 0（未接通/进行中）。
func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fromMillis Unix 毫秒 → time.Time，0 还原为零值。
func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// truncate 按 rune 截断不可信文本（号码/显示名来自 SIP 报文）。
func truncate(s string) string {
	r := []rune(s)
	if len(r) <= maxTextLen {
		return s
	}
	return string(r[:maxTextLen])
}
