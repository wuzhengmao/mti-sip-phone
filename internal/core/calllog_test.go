package core

// 通话记录采集（REQ-CALL-007）的回归测试：开始/接通/结束三个时刻在核心层
// 直接入队（不走事件总线——总线满即丢），且必须先写记录再发事件。

import (
	"context"
	"errors"
	"testing"
	"time"

	"mti-sip-phone/internal/config"
)

// fakeCallLog 记录收到的调用，供断言。
// 真实存储的幂等（同一通话重复结束只认第一次）由 internal/calllog 负责，
// 这里只验证核心层把哪些信息、按什么顺序送了下来。
type fakeCallLog struct {
	calls    []string // 调用顺序："start:c1" / "answered:c1" / "finish:c1"
	starts   []CallRecord
	answered []string
	finishes []finishArgs
}

type finishArgs struct {
	callID string
	at     time.Time
	reason string
	missed bool
}

func (f *fakeCallLog) Start(r CallRecord) {
	f.calls = append(f.calls, "start:"+r.CallID)
	f.starts = append(f.starts, r)
}

func (f *fakeCallLog) MarkAnswered(callID string, at time.Time) {
	f.calls = append(f.calls, "answered:"+callID)
	f.answered = append(f.answered, callID)
}

func (f *fakeCallLog) Finish(callID string, at time.Time, reason string, missed bool) {
	f.calls = append(f.calls, "finish:"+callID)
	f.finishes = append(f.finishes, finishArgs{callID, at, reason, missed})
}

func (f *fakeCallLog) Query(CallLogFilter) (CallLogPage, error) { return CallLogPage{}, nil }
func (f *fakeCallLog) Delete([]string) (int, error)             { return 0, nil }
func (f *fakeCallLog) Clear() (int, error)                      { return 0, nil }
func (f *fakeCallLog) Close() error                             { return nil }

// fakeTel 只实现 Dial/DialVideo：其余方法由内嵌的 nil 接口兜底
// （测试里不会调用，调用了会 panic——这正是我们想要的"没被用到"信号）。
type fakeTel struct {
	Telephony
	id       string
	dialErr  error
	videoErr error
}

func (f *fakeTel) Dial(context.Context, string) (string, error) {
	if f.dialErr != nil {
		return "", f.dialErr
	}
	return f.id, nil
}

func (f *fakeTel) DialVideo(context.Context, string) (string, error) {
	if f.videoErr != nil {
		return "", f.videoErr
	}
	return f.id, nil
}

func newCallLogTestApp(tel Telephony) (*App, *fakeCallLog) {
	app := New(config.Default(), tel, NewBus())
	log := &fakeCallLog{}
	app.SetCallLog(log)
	return app, log
}

// TestCallLogIncomingLifecycle 呼入接通：开始（来电）→ 接通 → 结束，
// 三个时刻各记一次且顺序正确。
func TestCallLogIncomingLifecycle(t *testing.T) {
	app, log := newCallLogTestApp(nil)

	app.OnIncoming("c1", "1002", "张三", true)
	app.OnEstablished("c1")
	app.OnTerminated("c1", "remote_hangup")

	if len(log.starts) != 1 {
		t.Fatalf("应有 1 条开始记录，得到 %d", len(log.starts))
	}
	rec := log.starts[0]
	if rec.CallID != "c1" || rec.Direction != "incoming" || rec.Remote != "1002" || rec.RemoteName != "张三" {
		t.Fatalf("开始记录字段不对: %+v", rec)
	}
	if !rec.HasVideo {
		t.Fatalf("带视频的来电应记 HasVideo: %+v", rec)
	}
	if rec.StartedAt.IsZero() {
		t.Fatalf("开始时刻未填写: %+v", rec)
	}
	if len(log.answered) != 1 || log.answered[0] != "c1" {
		t.Fatalf("应记一次接通，得到 %v", log.answered)
	}
	if len(log.finishes) != 1 {
		t.Fatalf("应有 1 条结束记录，得到 %d", len(log.finishes))
	}
	fin := log.finishes[0]
	if fin.reason != "remote_hangup" || fin.missed {
		t.Fatalf("结束记录不对: %+v", fin)
	}
	if fin.at.IsZero() {
		t.Fatalf("结束时刻未填写: %+v", fin)
	}
	want := []string{"start:c1", "answered:c1", "finish:c1"}
	if len(log.calls) != len(want) {
		t.Fatalf("调用序列应为 %v，得到 %v", want, log.calls)
	}
	for i := range want {
		if log.calls[i] != want[i] {
			t.Fatalf("调用序列应为 %v，得到 %v", want, log.calls)
		}
	}
}

// TestCallLogMissedIncoming missed 的口径必须与未接来电通知一致
// （本端拒接、振铃中本端挂断不算未接）。
func TestCallLogMissedIncoming(t *testing.T) {
	cases := []struct {
		reason string
		missed bool
	}{
		{"remote_cancel", true},
		{"remote_hangup", true},
		{"rejected", false},
		{"local_hangup", false},
	}
	for _, c := range cases {
		app, log := newCallLogTestApp(nil)
		app.OnIncoming("c1", "1002", "", false)
		app.OnTerminated("c1", c.reason)

		if len(log.finishes) != 1 {
			t.Fatalf("%s: 应有 1 条结束记录，得到 %d", c.reason, len(log.finishes))
		}
		if got := log.finishes[0].missed; got != c.missed {
			t.Fatalf("%s: missed 应为 %v，得到 %v", c.reason, c.missed, got)
		}
	}
}

// TestCallLogOutgoingDial 呼出的开始记录：号码取自呼叫目标，
// 视频呼叫要带 HasVideo。
func TestCallLogOutgoingDial(t *testing.T) {
	app, log := newCallLogTestApp(&fakeTel{id: "c9"})
	if _, err := app.Dial(context.Background(), "1001"); err != nil {
		t.Fatalf("呼出失败: %v", err)
	}
	if len(log.starts) != 1 {
		t.Fatalf("应有 1 条开始记录，得到 %d", len(log.starts))
	}
	if rec := log.starts[0]; rec.Direction != "outgoing" || rec.Remote != "1001" || rec.HasVideo {
		t.Fatalf("呼出开始记录字段不对: %+v", rec)
	}

	app2, log2 := newCallLogTestApp(&fakeTel{id: "c10"})
	if _, err := app2.DialVideo(context.Background(), "1002"); err != nil {
		t.Fatalf("视频呼出失败: %v", err)
	}
	if rec := log2.starts[0]; !rec.HasVideo || rec.Direction != "outgoing" {
		t.Fatalf("视频呼出应记 HasVideo: %+v", rec)
	}
}

// TestCallLogNoRecordOnDialFailure 同步拨号失败（未注册、媒体引擎建不起来等）
// 说明 INVITE 根本没发出去，不进通话记录。
func TestCallLogNoRecordOnDialFailure(t *testing.T) {
	app, log := newCallLogTestApp(&fakeTel{dialErr: errors.New("未注册")})
	if _, err := app.Dial(context.Background(), "1001"); err == nil {
		t.Fatal("应返回错误")
	}
	if len(log.starts) != 0 {
		t.Fatalf("拨号失败不该产生记录，得到 %d 条", len(log.starts))
	}
	// 电话能力未接入（tel == nil）同理
	app2, log2 := newCallLogTestApp(nil)
	if _, err := app2.Dial(context.Background(), "1001"); err == nil {
		t.Fatal("电话能力未接入时应返回错误")
	}
	if len(log2.starts) != 0 {
		t.Fatalf("拨号失败不该产生记录，得到 %d 条", len(log2.starts))
	}
}

// TestCallLogFinishReportedTwice 未接通的呼出会被 SIP 层上报两次结束
// （OnFailed 之后 defer 还会再来一次 OnTerminated）：核心层如实各记一次，
// **幂等由存储层按 ended_at=0 过滤**（勿在核心层再加"是否首次"的状态）。
func TestCallLogFinishReportedTwice(t *testing.T) {
	app, log := newCallLogTestApp(&fakeTel{id: "c1"})
	if _, err := app.Dial(context.Background(), "1001"); err != nil {
		t.Fatalf("呼出失败: %v", err)
	}
	app.OnFailed("c1", "486 Busy Here")
	app.OnTerminated("c1", "failed")

	if len(log.finishes) != 2 {
		t.Fatalf("两次结束上报都应到达存储层，得到 %d 次", len(log.finishes))
	}
	if log.finishes[0].reason != "486 Busy Here" {
		t.Fatalf("第一次上报的原因应原样透传，得到 %q", log.finishes[0].reason)
	}
}

// TestCallLogUnavailable 未接入存储（数据库打开失败）时，界面侧查询返回
// 明确错误，采集侧静默跳过。
func TestCallLogUnavailable(t *testing.T) {
	app := New(config.Default(), nil, NewBus())
	if _, err := app.QueryCallLog(CallLogFilter{}); !errors.Is(err, ErrCallLogUnavailable) {
		t.Fatalf("查询应返回 ErrCallLogUnavailable，得到 %v", err)
	}
	if _, err := app.DeleteCallLogRecords([]string{"c1"}); !errors.Is(err, ErrCallLogUnavailable) {
		t.Fatalf("删除应返回 ErrCallLogUnavailable，得到 %v", err)
	}
	if _, err := app.ClearCallLog(); !errors.Is(err, ErrCallLogUnavailable) {
		t.Fatalf("清空应返回 ErrCallLogUnavailable，得到 %v", err)
	}
	// 未接入时采集不得 panic
	app.OnIncoming("c1", "1002", "", false)
	app.OnEstablished("c1")
	app.OnTerminated("c1", "remote_hangup")
}
