package main

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// testClaudeChat：不起进程的 claude 对话，stdout 的行直接喂给 driver；通知收进 notices
func testClaudeChat(t *testing.T) (*Chat, *claudeDriver, func() []Notice) {
	var mu sync.Mutex
	var notices []Notice
	c := &Chat{
		id: "c1", agent: agentClaude, project: "p", title: "修 bug", state: "idle",
		entries: []Entry{}, pending: []*PermissionReq{}, subs: map[*subscriber]struct{}{},
		done: make(chan struct{}), stdin: nopWriteCloser{io.Discard},
		onNotice: func(n Notice) { mu.Lock(); notices = append(notices, n); mu.Unlock() },
	}
	d := newClaudeDriver(c, "claude", func(r json.RawMessage) json.RawMessage { return r })
	c.drv = d
	t.Cleanup(func() { close(c.done) }) // 让 refreshContext 之类等控制应答的 goroutine 退出
	return c, d, func() []Notice {
		time.Sleep(20 * time.Millisecond) // noticeLocked 是 go 出去的
		mu.Lock()
		defer mu.Unlock()
		return append([]Notice(nil), notices...)
	}
}

func feed(d *claudeDriver, lines ...string) {
	for _, l := range lines {
		d.handleLine([]byte(l))
	}
}

const (
	bgStart = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"b1","task_type":"local_bash","description":"sleep 8"}]}`
	bgEnd   = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`
	reply   = `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"都改好了"}]}}`
	success = `{"type":"result","subtype":"success","is_error":false}`
)

// 后台命令还在跑：这一轮回完了显示「后台运行中」、不报完成；它跑完 CLI 自己开一轮，那一轮结束才报一次完成
func TestBackgroundTaskThenAutoTurn(t *testing.T) {
	c, d, notices := testClaudeChat(t)
	c.mu.Lock()
	c.turns, c.state = 1, "running" // 页面发了一条消息
	c.mu.Unlock()

	feed(d, bgStart, reply, success, `{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	if info := c.Info(); info.Status != "background" || len(info.Background) != 1 || info.State != "idle" {
		t.Fatalf("后台任务在跑: %+v", info)
	}
	if n := notices(); len(n) != 0 {
		t.Fatalf("后台任务没跑完不该报完成: %+v", n)
	}

	// 后台任务跑完，CLI 自己开了一轮
	feed(d, bgEnd, `{"type":"system","subtype":"session_state_changed","state":"running"}`, `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`)
	if info := c.Info(); info.State != "running" || info.Status != "running" {
		t.Fatalf("CLI 自己开的一轮也是运行中: %+v", info)
	}
	feed(d, reply, success, `{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	if info := c.Info(); info.State != "idle" || info.Status != "done" {
		t.Fatalf("跑完了: %+v", info)
	}
	n := notices()
	if len(n) != 1 || n[0].Kind != "done" || n[0].Title != "Claude 完成了" || !strings.HasPrefix(n[0].Body, "都改好了\n修 bug · p") {
		t.Fatalf("应该只报一次完成: %+v", n)
	}
}

// 老版本 CLI 不报 session_state_changed：靠每一轮开头的 init 也知道它自己开了一轮
func TestAutoTurnFromInit(t *testing.T) {
	c, d, _ := testClaudeChat(t)
	feed(d, `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`)
	if c.Info().State != "running" {
		t.Fatal("init 之后应该是运行中")
	}
	feed(d, success)
	if c.Info().State != "idle" {
		t.Fatal("result 之后回到空闲")
	}
}

func TestNoticeWaitingAndError(t *testing.T) {
	c, d, notices := testClaudeChat(t)
	c.mu.Lock()
	c.turns, c.state = 1, "running"
	c.mu.Unlock()
	feed(d, `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build"}}}`)
	feed(d, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"turn limit"}`)
	n := notices()
	if len(n) != 1 || n[0].Kind != "waiting" || n[0].Title != "Claude 等你批准 Bash" || !strings.HasPrefix(n[0].Body, "rm -rf build") {
		t.Fatalf("等你批准: %+v", n)
	}
	// 请求还挂着时不报出错（状态是等你回答）；批掉以后下一轮出错才报
	c.mu.Lock()
	c.pending = []*PermissionReq{}
	c.turns = 1
	c.mu.Unlock()
	feed(d, `{"type":"result","subtype":"error_during_execution","is_error":true}`) // 被中断：不报
	c.mu.Lock()
	c.turns = 1
	c.mu.Unlock()
	feed(d, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"turn limit"}`)
	n = notices()
	if len(n) != 2 || n[1].Kind != "error" || !strings.Contains(n[1].Body, "error_max_turns") {
		t.Fatalf("出错: %+v", n)
	}
}

// 有人正开着这个对话（页面在前台）就不发
func TestNoticeSkippedWhileWatched(t *testing.T) {
	c, d, notices := testClaudeChat(t)
	s, _ := c.Subscribe()
	c.watch(s, true)
	c.mu.Lock()
	c.turns, c.state = 1, "running"
	c.mu.Unlock()
	feed(d, reply, success)
	if n := notices(); len(n) != 0 {
		t.Fatalf("有人看着不该发: %+v", n)
	}
	c.watch(s, false)
	c.mu.Lock()
	c.turns, c.state = 1, "running"
	c.mu.Unlock()
	feed(d, reply, success)
	if n := notices(); len(n) != 1 {
		t.Fatalf("切走了就该发: %+v", n)
	}
}

// CLI 自己开的一轮里页面又发了消息：那一轮的 result 不能算到这条消息头上
func TestSendDuringAutoTurn(t *testing.T) {
	c, d, _ := testClaudeChat(t)
	feed(d, `{"type":"system","subtype":"session_state_changed","state":"running"}`)
	c.mu.Lock()
	c.turns++ // 页面发了一条
	c.mu.Unlock()
	feed(d, reply, success)
	if info := c.Info(); info.State != "running" {
		t.Fatalf("页面发的那条还没处理，应该还是运行中: %+v", info)
	}
	feed(d, reply, success)
	if info := c.Info(); info.State != "idle" {
		t.Fatalf("都处理完了: %+v", info)
	}
}

// 跑工具时又发了一条：CLI 把它并进当前这一轮，只回一个 result。以它报的 idle 为准，不能一直停在运行中
func TestMergedMessageIdle(t *testing.T) {
	c, d, notices := testClaudeChat(t)
	c.mu.Lock()
	c.turns, c.state = 2, "running" // 页面发了两条，第二条是在跑工具时发的
	c.mu.Unlock()
	feed(d, `{"type":"system","subtype":"session_state_changed","state":"running"}`, reply, success)
	if c.Info().State != "running" {
		t.Fatal("还没收到 idle，照旧算运行中")
	}
	if n := notices(); len(n) != 0 {
		t.Fatalf("还在运行中，不该报完成: %+v", n)
	}
	feed(d, `{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	if info := c.Info(); info.State != "idle" || info.Status != "done" {
		t.Fatalf("CLI 说闲下来了: %+v", info)
	}
	if n := notices(); len(n) != 1 || n[0].Kind != "done" {
		t.Fatalf("应该补一条完成: %+v", n)
	}
	// 正常的一轮：result 时已经空闲，之后的 idle 不再重复报
	c.mu.Lock()
	c.turns, c.state = 1, "running"
	c.mu.Unlock()
	feed(d, reply, success, `{"type":"system","subtype":"session_state_changed","state":"idle"}`)
	if n := notices(); len(n) != 2 {
		t.Fatalf("一轮只报一次: %+v", n)
	}
}

// 老版本 CLI 不报 session_state_changed：result 之后一直没动静，过一会儿也回到空闲；马上开了下一轮的不算
func TestMergedMessageSettleFallback(t *testing.T) {
	old := settleDelay
	settleDelay = 50 * time.Millisecond
	defer func() { settleDelay = old }()

	c, d, _ := testClaudeChat(t)
	c.mu.Lock()
	c.turns, c.state = 2, "running"
	c.mu.Unlock()
	feed(d, reply, success)
	feed(d, `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`) // 排着的那条真开了一轮
	time.Sleep(120 * time.Millisecond)
	if c.Info().State != "running" {
		t.Fatal("下一轮开了，应该还在运行中")
	}
	feed(d, reply, success) // 这一轮结束，页面记的轮数正好归零
	c.mu.Lock()
	c.turns, c.state = 2, "running"
	c.mu.Unlock()
	feed(d, reply, success) // 并进来的那条没有自己的 result
	time.Sleep(120 * time.Millisecond)
	if c.Info().State != "idle" {
		t.Fatal("一直没动静，应该回到空闲")
	}
}
