package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// 网页聊天：每个对话是一个常驻的 CLI 子进程 —— claude 的 `-p --input-format stream-json`、
// codex 的 `app-server`、grok 的 `agent stdio`。协议各不相同，由 driver 负责翻译（chat_*.go），
// 这里管的是和协议无关的部分：对话状态、快照、订阅者、权限请求、进程生命周期。
//
// 进程和浏览器连接解耦 —— 关页面、手机锁屏、网断了，活照跑；重新连上时先收一份快照
// （到目前为止的全部 Entry + 还没批的权限请求），再接着收增量。这一层顶替了终端方案里
// tmux（进程不随连接死）+ mosh（断线重连）的职责。
//
// 工具权限都在页面上批：CLI 发来审批请求，页面上点「允许 / 拒绝」后回给它。所以网页聊天默认
// 不需要 --dangerously-skip-permissions 这一类开关。

var (
	permissionModes = map[string]bool{
		"default": true, "acceptEdits": true, "plan": true, "auto": true, "dontAsk": true, "bypassPermissions": true,
	}
	modelRe = regexp.MustCompile(`^[A-Za-z0-9._\-\[\]]{0,64}$`)
	efforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
)

// driver 是一种 CLI 的对话协议。方法都在 Chat 的锁外调用；要改对话状态时自己拿 c.mu。
type driver interface {
	command(o StartOpts) *exec.Cmd            // 要起的进程（参数、目录、环境）
	begin(o StartOpts)                        // 进程起来后：握手、开会话。不能阻塞，慢的放 goroutine
	handleLine(line []byte)                   // 进程 stdout 的一行
	prompt(text string, atts []*Upload) error // 发一条用户消息（页面上的 Entry 已经由 Chat 记好了）；text 里已经列了附件路径，atts 里的图片要不要另外直接给模型看由 driver 定
	interrupt() error                         // 打断当前这一轮，排队的一起作废
	setModel(model string) error
	setMode(mode string) error
	setEffort(effort string) error
}

type PermissionReq struct {
	ID          string            `json:"id"`
	Tool        string            `json:"tool"`
	Input       json.RawMessage   `json:"input"`
	Description string            `json:"description,omitempty"`
	BlockedPath string            `json:"blockedPath,omitempty"`
	CanAlways   bool              `json:"canAlways"`
	Always      *PermissionAlways `json:"always,omitempty"` // 「总是允许」会写下的规则（见 permission_label.go）

	answer func(PermissionAnswer) error // 把页面上的选择回给 CLI，由 driver 提供

	rawInput    json.RawMessage // claude：回 updatedInput 必须用原文，Input 可能被截断过
	suggestions json.RawMessage
}

type ChatInfo struct {
	ID             string `json:"id"`
	Agent          string `json:"agent"` // claude | codex | grok
	Project        string `json:"project"`
	Cwd            string `json:"cwd"`
	SessionID      string `json:"sessionId"`
	Title          string `json:"title"`
	State          string `json:"state"` // idle | running | exited
	PermissionMode string `json:"permissionMode"`
	Model          string `json:"model"`                 // 选的（别名，如 opus / default）
	ResolvedModel  string `json:"resolvedModel"`         // CLI 解析成的全名，请求就发给它
	ActualModel    string `json:"actualModel,omitempty"` // 最近一条回复实际是哪个模型（claude），和上面不一样就是被降级 / 换了
	Effort         string `json:"effort"`                // claude 没选的时候也是具体的一档：CLI 实际在用的（get_settings）
	// 侧栏显示用：running 运行中 / waiting 等你回答 / background 这一轮完了、后台任务还在跑 /
	// done 已完成（还没看）/ idle 空闲 / error 出错
	Status     string   `json:"status"`
	Background []BgTask `json:"background,omitempty"` // 还在跑的后台任务：后台命令、后台子代理
	Activity   string   `json:"activity"`             // 运行中具体在干嘛：thinking / text / tool:<名字>
	CreatedAt  int64    `json:"createdAt"`
	ExitCode   int      `json:"exitCode"`
	Clients    int      `json:"clients"`
	Pending    int      `json:"pending"`
}

type Chat struct {
	id, project, cwd, agent string
	created                 time.Time

	mu            sync.Mutex
	mode, model   string
	effort        string
	resolvedModel string
	actualModel   string
	catalog       json.RawMessage // 可选模型、斜杠命令（claude 是 initialize 的应答，其余由 driver 拼）
	outcome       string          // 最近一轮的结果：ok / interrupted / error
	unread        bool            // 这一轮结束后还没人看过
	activity      string
	suggestion    string          // CLI 预测的下一句（claude 的 prompt_suggestion），输入框里淡色显示
	context       json.RawMessage // 上下文用量（claude 的 get_context_usage，裁剪过），输入框下方常驻显示
	stopping      bool            // 是我们自己结束的进程，退出码不算出错
	autoTurn      bool            // CLI 自己开的一轮（后台任务跑完了，它接着处理结果），不是页面发的消息
	background    []BgTask
	sessionID     string
	title         string
	state         string
	turns         int // 已发出、还没结束的消息数（运行中也能继续发，排队）
	exitCode      int
	entries       []Entry
	pending       []*PermissionReq
	stderr        []string
	subs          map[*subscriber]struct{}
	lastSeen      time.Time

	onPrompt func(agent, cwd, sessionID, text string)
	onNotice func(Notice)

	drv   driver
	cmd   *exec.Cmd
	wmu   sync.Mutex // 串行化写 stdin：用户消息、权限回复、中断共用这一条管道
	stdin io.WriteCloser
	done  chan struct{}
}

type subscriber struct {
	ch      chan []byte
	once    sync.Once
	watched bool // 这个页面在前台、有焦点（页面报的）：有人盯着就不发通知
}

// BgTask：CLI 报的一个后台任务（system/background_tasks_changed）
type BgTask struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"` // local_bash / local_agent / …
	Description string `json:"description"`
}

func (s *subscriber) close() { s.once.Do(func() { close(s.ch) }) }

type StartOpts struct {
	Agent          string   `json:"agent"`
	Project        string   `json:"project"`
	SessionID      string   `json:"sessionId"`
	PermissionMode string   `json:"permissionMode"`
	Model          string   `json:"model"`
	Effort         string   `json:"effort"`
	Prompt         string   `json:"prompt"`      // 起来就发的第一条消息
	Attachments    []string `json:"attachments"` // 第一条消息带的附件（见 uploads.go）

	cwd     string
	title   string
	history []Entry
}

type ChatManager struct {
	cfg       *Config
	mu        sync.Mutex
	chats     map[string]*Chat
	onCatalog func(agent, cwd string, raw json.RawMessage) json.RawMessage // 顺手更新 /api/catalog 的缓存，返回补全过的
	onPrompt  func(agent, cwd, sessionID, text string)                     // 网页发的消息也记进 CLI 的输入历史
	claudeBin func() string                                                // 起 claude 用哪个程序（可能换成了 reclaude）
	onNotice  func(Notice)                                                 // 完成 / 等你回答 / 出错时发通知（见 notify.go）
}

func newChatManager(cfg *Config) *ChatManager {
	m := &ChatManager{cfg: cfg, chats: map[string]*Chat{}, claudeBin: func() string { return cfg.ClaudeBin }}
	go m.reap()
	return m
}

func (m *ChatManager) newDriver(c *Chat) driver {
	catalog := func(raw json.RawMessage) json.RawMessage {
		if m.onCatalog != nil {
			return m.onCatalog(c.agent, c.cwd, raw)
		}
		return raw
	}
	switch c.agent {
	case agentCodex:
		return newCodexDriver(c, m.cfg, catalog)
	case agentGrok:
		return newGrokDriver(c, m.cfg, catalog)
	}
	return newClaudeDriver(c, m.claudeBin(), catalog)
}

func (m *ChatManager) Start(o StartOpts) (*Chat, error) {
	if err := validateStart(&o); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 同一个会话只允许一个进程往里写，已经在跑就直接接上
	if o.SessionID != "" {
		for _, c := range m.chats {
			c.mu.Lock()
			same := c.sessionID == o.SessionID && c.state != "exited"
			c.mu.Unlock()
			if same {
				return c, nil
			}
		}
	}

	c := &Chat{
		id: newID(), agent: o.Agent, project: o.Project, cwd: o.cwd, mode: o.PermissionMode, model: o.Model, effort: o.Effort,
		created: time.Now(), sessionID: o.SessionID, title: o.title, state: "idle",
		entries: o.history, pending: []*PermissionReq{}, stderr: []string{},
		subs: map[*subscriber]struct{}{}, lastSeen: time.Now(),
		done: make(chan struct{}), onPrompt: m.onPrompt, onNotice: m.onNotice,
	}
	if c.entries == nil {
		c.entries = []Entry{}
	}
	c.drv = m.newDriver(c)
	cmd := c.drv.command(o)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 结束时连 CLI 起的子进程一起收掉
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 %s 失败: %w", o.Agent, err)
	}
	c.cmd, c.stdin = cmd, stdin
	m.chats[c.id] = c
	log.Printf("chat %s: 启动 %s pid=%d cwd=%s resume=%s mode=%s", c.id, o.Agent, cmd.Process.Pid, o.cwd, o.SessionID, o.PermissionMode)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = eachLine(stdout, 64<<20, func(line []byte) bool { c.drv.handleLine(line); return true })
	}()
	go func() {
		defer wg.Done()
		_ = eachLine(stderr, 64<<10, func(line []byte) bool { c.handleStderr(string(line)); return true })
	}()
	go func() {
		wg.Wait()
		err := cmd.Wait()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		log.Printf("chat %s: %s 退出 code=%d err=%v", c.id, c.agent, code, err)
		c.mu.Lock()
		c.state, c.exitCode, c.turns, c.lastSeen, c.activity = "exited", code, 0, time.Now(), ""
		c.autoTurn, c.background = false, nil
		if code != 0 && !c.stopping {
			c.outcome = "error"
			detail := ""
			if n := len(c.stderr); n > 0 {
				detail = " — " + oneLine(c.stderr[n-1], 160)
			}
			c.noticeLocked("error", fmt.Sprintf("%s 进程退出了（code %d）%s", agentName(c.agent), code, detail))
		}
		c.pending = []*PermissionReq{}
		c.addEntryLocked(noteEntry(fmt.Sprintf("%s 进程已退出（code %d）", c.agent, code), ""))
		c.broadcastLocked(map[string]any{"type": "state", "chat": c.infoLocked()})
		c.mu.Unlock()
		close(c.done)
	}()

	c.drv.begin(o)
	if strings.TrimSpace(o.Prompt) != "" || len(o.Attachments) > 0 {
		if err := c.Send(o.Prompt, o.Attachments); err != nil {
			log.Printf("chat %s: 发第一条消息失败: %v", c.id, err)
		}
	}
	return c, nil
}

func (m *ChatManager) Get(id string) *Chat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chats[id]
}

func (m *ChatManager) List() []ChatInfo {
	m.mu.Lock()
	chats := make([]*Chat, 0, len(m.chats))
	for _, c := range m.chats {
		chats = append(chats, c)
	}
	m.mu.Unlock()
	out := make([]ChatInfo, 0, len(chats))
	for _, c := range chats {
		out = append(out, c.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// BySession：会话 ID → 正在跑它的网页对话，会话列表上据此标「运行中」
func (m *ChatManager) BySession() map[string]string {
	out := map[string]string{}
	for _, c := range m.List() {
		if c.SessionID != "" && c.State != "exited" {
			out[c.SessionID] = c.ID
		}
	}
	return out
}

// Close 结束进程并把对话从列表里拿掉
func (m *ChatManager) Close(id string) bool {
	m.mu.Lock()
	c := m.chats[id]
	delete(m.chats, id)
	m.mu.Unlock()
	if c == nil {
		return false
	}
	c.Stop()
	c.mu.Lock()
	c.broadcastLocked(map[string]any{"type": "closed"})
	for s := range c.subs {
		s.close()
	}
	c.subs = map[*subscriber]struct{}{}
	c.mu.Unlock()
	return true
}

func (m *ChatManager) StopAll() {
	m.mu.Lock()
	chats := make([]*Chat, 0, len(m.chats))
	for _, c := range m.chats {
		chats = append(chats, c)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range chats {
		wg.Add(1)
		go func() { defer wg.Done(); c.Stop() }()
	}
	wg.Wait()
}

// reap：没人看着、也没在干活的对话，闲置久了就结束进程（会话记录还在，随时能续）；
// 已退出的过一阵从列表里清掉。正在跑的永远不动。
func (m *ChatManager) reap() {
	idle := 12 * time.Hour
	if h, err := time.ParseDuration(os.Getenv("RCWEB_CHAT_IDLE")); err == nil && h > 0 {
		idle = h
	}
	for range time.Tick(5 * time.Minute) {
		m.mu.Lock()
		chats := make([]*Chat, 0, len(m.chats))
		for _, c := range m.chats {
			chats = append(chats, c)
		}
		m.mu.Unlock()
		for _, c := range chats {
			c.mu.Lock()
			quiet := len(c.subs) == 0 && len(c.pending) == 0
			age := time.Since(c.lastSeen)
			state := c.state
			c.mu.Unlock()
			switch {
			case quiet && state == "exited" && age > 30*time.Minute:
				m.Close(c.id)
			case quiet && state == "idle" && age > idle:
				log.Printf("chat %s: 闲置 %s，结束进程", c.id, age.Round(time.Minute))
				c.Stop()
			}
		}
	}
}

// ---- 单个对话 ----

func (c *Chat) Info() ChatInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.infoLocked()
}

func (c *Chat) infoLocked() ChatInfo {
	return ChatInfo{
		ID: c.id, Agent: c.agent, Project: c.project, Cwd: c.cwd, SessionID: c.sessionID, Title: c.title,
		State: c.state, PermissionMode: c.mode, Model: c.model, ResolvedModel: c.resolvedModel, ActualModel: c.actualModel,
		Effort: c.effort, Status: c.statusLocked(), Background: c.background, Activity: c.activity, CreatedAt: c.created.UnixMilli(),
		ExitCode: c.exitCode, Clients: len(c.subs), Pending: len(c.pending),
	}
}

func (c *Chat) statusLocked() string {
	switch {
	case len(c.pending) > 0:
		return "waiting"
	case c.state == "running":
		return "running"
	case len(c.background) > 0:
		return "background" // 这一轮回完了，但后台命令 / 子代理还在跑，跑完 CLI 会自己接着处理
	case c.outcome == "error":
		return "error" // 出错一直挂着，直到下一轮开始
	case c.unread && c.outcome == "ok":
		return "done"
	}
	return "idle"
}

// Seen：有人在看这个对话了，「已完成」转成「空闲」
func (c *Chat) Seen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unread {
		c.unread = false
		c.broadcastLocked(map[string]any{"type": "state", "chat": c.infoLocked()})
	}
}

func (c *Chat) setActivityLocked(a string) {
	if a != c.activity {
		c.activity = a
		c.broadcastLocked(map[string]any{"type": "activity", "activity": a})
	}
}

func (c *Chat) broadcastLocked(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	for s := range c.subs {
		select {
		case s.ch <- b:
		default:
			// 客户端跟不上：断开它，让它重连拿快照，而不是让整个对话等它
			delete(c.subs, s)
			s.close()
		}
	}
}

func (c *Chat) broadcastStateLocked() {
	c.broadcastLocked(map[string]any{"type": "state", "chat": c.infoLocked()})
}

func (c *Chat) addEntryLocked(e Entry) {
	c.entries = append(c.entries, e)
	c.broadcastLocked(map[string]any{"type": "entry", "entry": e})
}

func (c *Chat) setCatalogLocked(raw json.RawMessage) {
	c.catalog = raw
	c.broadcastLocked(map[string]any{"type": "catalog", "catalog": raw})
}

func (c *Chat) addPendingLocked(p *PermissionReq) {
	c.pending = append(c.pending, p)
	c.broadcastLocked(map[string]any{"type": "permission", "req": p})
	c.broadcastStateLocked()
	c.noticeLocked("waiting", pendingText(p))
}

// removePendingLocked：CLI 那边撤回了请求（中断、超时、别处批了），页面上的卡片也收起来
func (c *Chat) removePendingLocked(id string) *PermissionReq {
	for i, p := range c.pending {
		if p.ID == id {
			c.pending = append(c.pending[:i:i], c.pending[i+1:]...)
			c.broadcastLocked(map[string]any{"type": "permission_done", "id": id})
			c.broadcastStateLocked()
			return p
		}
	}
	return nil
}

func (c *Chat) setStateLocked() {
	st := "idle"
	if c.state == "exited" {
		return
	}
	if c.turns > 0 || c.autoTurn {
		st = "running"
	}
	if st != c.state {
		c.state = st
		c.broadcastStateLocked()
	}
}

// endTurnLocked：一轮结束。outcome 是 ok / interrupted / error；note 非空就在对话里插一条提示。
// 被中断时排队的消息也一起作废（和 claude 的行为一致）
func (c *Chat) endTurnLocked(outcome, note string, cost float64, duration time.Duration) {
	c.outcome, c.unread, c.activity = outcome, true, ""
	if outcome == "interrupted" {
		c.turns = 0
	} else if c.turns > 0 {
		c.turns--
	}
	if note != "" {
		c.addEntryLocked(noteEntry(note, ""))
	}
	subtype := map[string]string{"ok": "success", "interrupted": "interrupted"}[outcome]
	if subtype == "" {
		subtype = "error"
	}
	c.broadcastLocked(map[string]any{
		"type": "result", "ok": outcome == "ok", "subtype": subtype, "cost": cost, "durationMs": duration.Milliseconds(),
	})
	c.setStateLocked()
	c.broadcastStateLocked()
	c.afterTurnLocked(note)
}

// afterTurnLocked：一轮结束后发不发通知。整个对话停下来了（没有排队的消息、没有等你批的、没有后台任务）才算完成；
// 后台任务还在跑时先不报，等它跑完、CLI 自己接着的那一轮结束再报。被你中断的不报
func (c *Chat) afterTurnLocked(note string) {
	if c.state != "idle" || len(c.pending) > 0 {
		return
	}
	switch c.outcome {
	case "ok":
		if len(c.background) == 0 {
			c.noticeLocked("done", c.lastReplyLocked())
		}
	case "error":
		c.noticeLocked("error", note)
	}
}

// setBackgroundLocked：后台任务列表变了（CLI 报的是完整的列表）。最后一个跑完后 CLI 会自己开一轮处理结果，
// 「完成」的通知等那一轮结束再发
func (c *Chat) setBackgroundLocked(tasks []BgTask) {
	if len(tasks) == 0 && len(c.background) == 0 {
		return
	}
	c.background = tasks
	c.broadcastStateLocked()
}

// lastReplyLocked：最近一条回复的正文，通知里带上
func (c *Chat) lastReplyLocked() string {
	for i := len(c.entries) - 1; i >= 0; i-- {
		e := c.entries[i]
		if e.Role == "user" && len(e.Blocks) > 0 && e.Blocks[0].T == "text" {
			break
		}
		if e.Role != "assistant" {
			continue
		}
		for j := len(e.Blocks) - 1; j >= 0; j-- {
			if b := e.Blocks[j]; b.T == "text" && strings.TrimSpace(b.Text) != "" {
				return oneLine(b.Text, 180)
			}
		}
	}
	return ""
}

// noticeLocked 发一条通知（kind：done / waiting / error）。有人正盯着这个对话的页面就不发
func (c *Chat) noticeLocked(kind, body string) {
	if c.onNotice == nil {
		return
	}
	for s := range c.subs {
		if s.watched {
			return
		}
	}
	who := agentName(c.agent)
	title := map[string]string{"done": who + " 完成了", "waiting": who + " 等你回答", "error": who + " 出错了"}[kind]
	if kind == "waiting" && len(c.pending) > 0 {
		title = pendingTitle(who, c.pending[len(c.pending)-1])
	}
	name := c.title
	if name == "" {
		name = "新对话"
	}
	n := Notice{Kind: kind, Title: title, Body: name + " · " + c.project, Chat: c.id}
	if body = strings.TrimSpace(body); body != "" {
		n.Body = body + "\n" + n.Body
	}
	go c.onNotice(n)
}

// watch：页面报它是不是在前台（切走、锁屏、切到别的对话都是 false）
func (c *Chat) watch(s *subscriber, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.subs[s]; ok {
		s.watched = on
	}
}

// fail：driver 起不来（握手失败、开会话失败）时用：留一条提示，结束进程，对话标成出错
func (c *Chat) fail(what string, err error) {
	log.Printf("chat %s: %s: %v", c.id, what, err)
	c.mu.Lock()
	c.outcome = "error"
	c.addEntryLocked(noteEntry(what+"："+oneLine(err.Error(), 300), ""))
	c.mu.Unlock()
	c.Stop()
}

func (c *Chat) Subscribe() (*subscriber, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &subscriber{ch: make(chan []byte, 1024)}
	c.subs[s] = struct{}{}
	c.lastSeen = time.Now()
	snap, _ := json.Marshal(c.snapshotLocked())
	return s, snap
}

// 快照只带最后一页、工具只带摘要（见 session_page.go）：手机锁屏再亮、网络一抖都会重连，每次都要收一份
func (c *Chat) snapshotLocked() map[string]any {
	p := pageOf(c.entries, 0, 0, pageSize)
	return map[string]any{
		"type": "snapshot", "chat": c.infoLocked(), "catalog": c.catalog, "suggestion": c.suggestion, "context": c.context,
		"entries": p.Entries, "start": p.Start, "more": p.More, "pending": c.pending, "stderr": c.stderr,
	}
}

func (c *Chat) Unsubscribe(s *subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, s)
	s.close()
	c.lastSeen = time.Now()
}

// sendTo 只发给一个客户端（比如它自己操作失败的提示）
func (c *Chat) sendTo(s *subscriber, v any) {
	b, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.subs[s]; ok {
		select {
		case s.ch <- b:
		default:
		}
	}
}

// write 往 CLI 的 stdin 写一行 JSON
func (c *Chat) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return errors.New(c.agent + " 进程已经不在了")
	}
	return nil
}

// Send 发一条用户消息，attachments 是先传上来的附件 id（见 uploads.go）
func (c *Chat) Send(text string, attachments []string) error {
	ups, err := loadUploads(attachments)
	if err != nil {
		return err
	}
	return c.send(text, ups, true)
}

// send 的 record=false 用于页面上的设置操作（比如 effort 下拉框发的 /effort），不进输入历史
func (c *Chat) send(text string, ups []*Upload, record bool) error {
	if strings.TrimSpace(text) == "" && len(ups) == 0 {
		return errors.New("消息是空的")
	}
	full := text
	if len(ups) > 0 {
		full = withAttachments(text, ups)
	}
	c.mu.Lock()
	if c.state == "exited" {
		c.mu.Unlock()
		return errors.New(c.agent + " 进程已退出，重新连接后再发")
	}
	// 先记进快照再交给 CLI：它的回应不可能早于这条用户消息出现在页面上。
	// 记的是交给 CLI 的原文（附件路径也在里面），和以后从会话记录里读出来的一样，页面上按同一套规则显示
	c.addEntryLocked(Entry{Role: "user", Blocks: []Block{{T: "text", Text: full}}, TS: time.Now().UTC().Format(time.RFC3339)})
	if c.title == "" && !strings.HasPrefix(text, "/") {
		c.title = oneLine(text, 60)
		if c.title == "" {
			c.title = ups[0].Name // 只发了附件
		}
	}
	c.turns++
	c.outcome, c.unread, c.suggestion = "", false, ""
	c.lastSeen = time.Now()
	c.setActivityLocked("thinking")
	c.setStateLocked()
	sid := c.sessionID
	c.mu.Unlock()

	if record && c.onPrompt != nil && strings.TrimSpace(text) != "" {
		c.onPrompt(c.agent, c.cwd, sid, text)
	}
	return c.drv.prompt(full, ups)
}

type PermissionAnswer struct {
	ID      string            `json:"id"`
	Choice  string            `json:"choice"` // allow | always | deny
	Answers map[string]string `json:"answers"`
	Message string            `json:"message"`
}

func (c *Chat) Respond(a PermissionAnswer) error {
	c.mu.Lock()
	req := c.removePendingLocked(a.ID)
	c.mu.Unlock()
	if req == nil {
		return errors.New("这个请求已经处理过了")
	}
	return req.answer(a)
}

// Interrupt 相当于在 TUI 里按 Esc：打断当前这一轮，进程留着，下一条消息照发
func (c *Chat) Interrupt() error { return c.drv.interrupt() }

func (c *Chat) SetModel(model string) error {
	if model == "" || !modelRe.MatchString(model) {
		return errors.New("模型名不合法")
	}
	return c.drv.setModel(model)
}

func (c *Chat) SetMode(mode string) error {
	if !validMode(c.agent, mode) {
		return fmt.Errorf("不认识的权限模式 %q", mode)
	}
	return c.drv.setMode(mode)
}

func (c *Chat) SetEffort(effort string) error {
	if !validEffort(c.agent, effort) {
		return fmt.Errorf("不认识的 effort %q", effort)
	}
	return c.drv.setEffort(effort)
}

// setLocked 改页面上显示的一项设置（模型 / 权限模式 / effort），变了才广播
func (c *Chat) setLocked(dst *string, v string) {
	if *dst != v {
		*dst = v
		c.broadcastStateLocked()
	}
}

func (c *Chat) Stop() {
	select {
	case <-c.done:
		return
	default:
	}
	c.mu.Lock()
	c.stopping = true
	c.mu.Unlock()
	pid := c.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-c.done
	}
}

func (c *Chat) handleStderr(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addStderrLocked(line)
}

func (c *Chat) addStderrLocked(line string) {
	c.stderr = append(c.stderr, line)
	if len(c.stderr) > 100 {
		c.stderr = c.stderr[len(c.stderr)-100:]
	}
	c.broadcastLocked(map[string]any{"type": "stderr", "line": line})
}

func noteEntry(text, ts string) Entry {
	return Entry{Role: "note", Blocks: []Block{{T: "text", Text: text}}, TS: ts}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- HTTP / WebSocket ----

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.chats.List())
}

func (s *Server) handleStartChat(w http.ResponseWriter, r *http.Request) {
	var o StartOpts
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&o); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	path, ok := s.projectPath(o.Project)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个项目")
		return
	}
	o.cwd = path
	// 附件不在了就别起进程：起来之后第一条消息发不出去，页面上只看到一个空对话
	if _, err := loadUploads(o.Attachments); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if o.SessionID != "" {
		info, ok := s.findSession(path, o.Agent, o.SessionID)
		if !ok {
			writeErr(w, http.StatusNotFound, "没有这个会话")
			return
		}
		// worktree / 子目录里的会话要回到那个目录里续，否则 CLI 按 cwd 找不到它
		if isDir(info.Cwd) {
			o.cwd = info.Cwd
		}
		o.title = info.Title
		// 多半刚在会话记录页打开过，缓存里有；Clip 让对话往后追加时另开数组，不碰缓存里那份
		if d, err := s.loadSession(info); err == nil {
			o.history = slices.Clip(d.entries)
		}
	} else if !isDir(path) {
		writeErr(w, http.StatusNotFound, "项目目录不存在: "+path)
		return
	}
	c, err := s.chats.Start(o)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, c.Info())
}

func (s *Server) handleCloseChat(w http.ResponseWriter, r *http.Request) {
	if !s.chats.Close(r.PathValue("id")) {
		writeErr(w, http.StatusNotFound, "没有这个对话")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleChatWS(w http.ResponseWriter, r *http.Request) {
	c := s.chats.Get(r.PathValue("id"))
	if c == nil {
		writeErr(w, http.StatusNotFound, "没有这个对话")
		return
	}
	// Origin 已经在 Auth.require 里按 Host / X-Forwarded-Host 校验过；库自带的校验只认 Host，放行
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionNoContextTakeover,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	sub, snap := c.Subscribe()
	defer c.Unsubscribe(sub)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		if conn.Write(ctx, websocket.MessageText, snap) != nil {
			return
		}
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case b, ok := <-sub.ch:
				if !ok {
					conn.Close(websocket.StatusTryAgainLater, "resync")
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Type        string   `json:"type"`
			Text        string   `json:"text"`
			Attachments []string `json:"attachments"`
			Model       string   `json:"model"`
			Mode        string   `json:"mode"`
			Effort      string   `json:"effort"`
			Visible     bool     `json:"visible"`
			PermissionAnswer
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		fail := func(err error) {
			if err != nil {
				c.sendTo(sub, map[string]string{"type": "error", "message": err.Error()})
			}
		}
		switch msg.Type {
		case "send":
			fail(c.Send(msg.Text, msg.Attachments))
		case "permission":
			fail(c.Respond(msg.PermissionAnswer))
		case "seen":
			c.Seen()
		case "presence":
			c.watch(sub, msg.Visible)
		// effort 不等应答（claude 是发一条 /effort），同步做，保证它排在紧接着发的消息前面
		case "set_effort":
			fail(c.SetEffort(msg.Effort))
		// 下面几个要等 CLI 应答，放到 goroutine 里，别让读循环卡住（pong 也靠它收）
		case "interrupt":
			go func() { fail(c.Interrupt()) }()
		case "set_model":
			go func() { fail(c.SetModel(msg.Model)) }()
		case "set_mode":
			go func() { fail(c.SetMode(msg.Mode)) }()
		}
	}
}
