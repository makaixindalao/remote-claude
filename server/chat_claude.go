package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// claude：`claude -p --input-format stream-json --output-format stream-json` 常驻进程。
//
// 工具权限走 `--permission-prompt-tool stdio`：claude 在 stdout 发 control_request/can_use_tool，
// 页面上点「允许 / 拒绝」后经 stdin 回 control_response。所以不需要 --dangerously-skip-permissions，
// 也就不需要 scc 那个 IS_SANDBOX=1 的 root 绕过。

var slashArg = regexp.MustCompile(`^/(model|effort|clear)(?:\s+(\S+))?\s*$`)

type claudeDriver struct {
	c         *Chat
	bin       string // 开对话时定下来：中途在设置里换成 reclaude，已经开着的对话不跟着变
	onCatalog func(json.RawMessage) json.RawMessage
	reqN      atomic.Int64

	// 以下由 c.mu 保护
	ctrl     map[string]chan ctrlResult
	clearing bool // 发了 /clear，等它这一轮结束就清空页面
	// CLI 报过 session_state_changed：它说 idle 时以它为准。老版本不报，就靠 result 之后等一会儿看它还动不动
	sawState bool
	ticks    int // 每开一轮 / 每个 API 调用加一：result 之后拿来看 CLI 是不是又动了
}

type ctrlResult struct {
	ok   bool
	data json.RawMessage
	err  string
}

func newClaudeDriver(c *Chat, bin string, onCatalog func(json.RawMessage) json.RawMessage) *claudeDriver {
	return &claudeDriver{c: c, bin: bin, onCatalog: onCatalog, ctrl: map[string]chan ctrlResult{}}
}

func (d *claudeDriver) command(o StartOpts) *exec.Cmd {
	args := []string{
		"--print", "--verbose",
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
		"--permission-mode", o.PermissionMode,
	}
	// default 就是不指定：跟着 Claude Code 自己的推荐走，出新模型也不用改这里
	if o.Model != "" && o.Model != "default" {
		args = append(args, "--model", o.Model)
	}
	if o.Effort != "" {
		args = append(args, "--effort", o.Effort)
	}
	if o.SessionID != "" {
		args = append(args, "--resume", o.SessionID)
	}
	// 让它报 session_state_changed（running / idle）：后台任务跑完后它会自己开一轮，靠这个才知道它又在干活了
	extra := []string{"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1"}
	// claude 拒绝以 root 身份 bypass 权限，和 scc 一样用 IS_SANDBOX=1 绕过（见 docs/03-scc.md）
	if o.PermissionMode == "bypassPermissions" && os.Geteuid() == 0 {
		extra = append(extra, "IS_SANDBOX=1")
	}
	// 附件存在项目外面（见 uploads.go），加进可访问的目录，读它们不用一个个批
	if os.MkdirAll(uploadDir(), 0o700) == nil {
		args = append(args, "--add-dir", uploadDir())
	}
	cmd := exec.Command(d.bin, args...)
	cmd.Dir = o.cwd
	cmd.Env = childEnv(extra...)
	return cmd
}

// begin 先问一遍 initialize：斜杠命令、可选模型都在应答里。要在第一条消息之前写进 stdin
// promptSuggestions：每轮结束后让 CLI 发一条预测的下一句（和 TUI 里输入框的暗色建议是同一个东西）
func (d *claudeDriver) begin(StartOpts) {
	c := d.c
	initID, initCh, err := d.startControl(map[string]any{"subtype": "initialize", "promptSuggestions": true})
	if err != nil {
		return
	}
	go func() {
		raw, err := d.awaitControl(initID, initCh, time.Minute)
		if err != nil {
			log.Printf("chat %s: initialize 失败: %v", c.id, err)
			return
		}
		raw = d.onCatalog(trimCatalog(raw)) // 带上各模型的默认 effort（/api/catalog 那边问过的话）
		c.mu.Lock()
		c.setCatalogLocked(raw)
		c.mu.Unlock()
		d.refreshSettings()
		d.refreshContext() // 续会话时一打开就知道上下文用了多少
	}()
}

func (d *claudeDriver) prompt(text string, atts []*Upload) error {
	c := d.c
	// /model、/effort 由 claude 自己处理（headless 下当场生效）；这里只同步页面上显示的值
	if m := slashArg.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
		c.mu.Lock()
		switch {
		case m[1] == "model" && m[2] != "" && modelRe.MatchString(m[2]):
			c.setLocked(&c.model, m[2])
		case m[1] == "effort" && efforts[m[2]]:
			c.setLocked(&c.effort, m[2])
		case m[1] == "clear":
			d.clearing = true
		}
		c.mu.Unlock()
	}
	// 图片放在文字前面（模型这样看得最好），文字里的 <attachments> 列着同样这几张的路径
	content := []map[string]any{}
	for _, u := range atts {
		if !u.Image {
			continue
		}
		data, err := os.ReadFile(u.vision)
		if err != nil {
			return fmt.Errorf("读附件 %s 失败: %w", u.Name, err)
		}
		content = append(content, map[string]any{
			"type": "image", "source": map[string]string{"type": "base64", "media_type": u.mime, "data": base64.StdEncoding.EncodeToString(data)},
		})
	}
	content = append(content, map[string]any{"type": "text", "text": text})
	return c.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
}

// buildDecision 把页面上的选择翻成控制协议的 decision：
//
//	allow  → {behavior:allow, updatedInput:<原参数>}
//	always → 同上 + updatedPermissions:<CLI 给的规则建议>
//	deny   → {behavior:deny, message, interrupt:false}（把拒绝告诉模型，不打断这一轮）
//
// AskUserQuestion 也走这条路：答案放进 updatedInput.answers（问题文本 → 选中的 label）。
func buildDecision(req *PermissionReq, a PermissionAnswer) map[string]any {
	if a.Choice == "deny" {
		msg := a.Message
		if msg == "" {
			msg = "The user declined this tool use."
		}
		return map[string]any{"behavior": "deny", "message": msg, "interrupt": false}
	}
	input := req.rawInput
	if req.Tool == "AskUserQuestion" && a.Answers != nil {
		var m map[string]any
		_ = json.Unmarshal(req.rawInput, &m)
		if m == nil {
			m = map[string]any{}
		}
		m["answers"] = a.Answers
		input, _ = json.Marshal(m)
	}
	d := map[string]any{"behavior": "allow", "updatedInput": input}
	if a.Choice == "always" && req.CanAlways {
		d["updatedPermissions"] = req.suggestions
	}
	return d
}

func (d *claudeDriver) answer(req *PermissionReq) func(PermissionAnswer) error {
	return func(a PermissionAnswer) error {
		return d.c.write(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype": "success", "request_id": req.ID, "response": buildDecision(req, a),
			},
		})
	}
}

func (d *claudeDriver) interrupt() error {
	_, err := d.control(map[string]any{"subtype": "interrupt"}, 10*time.Second)
	return err
}

// setModel / setMode 走控制协议，当场生效、不在会话里留痕
func (d *claudeDriver) setModel(model string) error {
	if _, err := d.control(map[string]any{"subtype": "set_model", "model": model}, 15*time.Second); err != nil {
		return err
	}
	c := d.c
	c.mu.Lock()
	// 上一条回复是旧模型回的，留着会被当成「降级」；等新模型回了再显示
	c.model, c.actualModel = model, ""
	c.broadcastStateLocked()
	c.mu.Unlock()
	d.refreshSettings() // 换了模型，全名和默认 effort 都可能跟着变
	return nil
}

func (d *claudeDriver) setMode(mode string) error {
	if _, err := d.control(map[string]any{"subtype": "set_permission_mode", "mode": mode}, 15*time.Second); err != nil {
		return err
	}
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	d.c.mode = mode
	d.c.broadcastStateLocked()
	return nil
}

// effort 没有对应的控制请求，用 headless 下当场生效的 /effort 命令
func (d *claudeDriver) setEffort(effort string) error {
	return d.c.send("/effort "+effort, nil, false)
}

// control 发一个控制请求并等它的 control_response。
// 拆成 start / await 两步，是为了让 begin 能保证 initialize 先于第一条用户消息写进 stdin。
func (d *claudeDriver) control(req map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id, ch, err := d.startControl(req)
	if err != nil {
		return nil, err
	}
	return d.awaitControl(id, ch, timeout)
}

func (d *claudeDriver) startControl(req map[string]any) (string, chan ctrlResult, error) {
	c := d.c
	id := fmt.Sprintf("rcweb-%d", d.reqN.Add(1))
	ch := make(chan ctrlResult, 1)
	c.mu.Lock()
	d.ctrl[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		c.mu.Lock()
		delete(d.ctrl, id)
		c.mu.Unlock()
		return "", nil, err
	}
	return id, ch, nil
}

func (d *claudeDriver) awaitControl(id string, ch chan ctrlResult, timeout time.Duration) (json.RawMessage, error) {
	c := d.c
	defer func() {
		c.mu.Lock()
		delete(d.ctrl, id)
		c.mu.Unlock()
	}()
	select {
	case r := <-ch:
		if !r.ok {
			return nil, errors.New("claude 拒绝了: " + r.err)
		}
		return r.data, nil
	case <-time.After(timeout):
		return nil, errors.New("claude 没有响应")
	case <-c.done:
		return nil, errors.New("claude 进程已经不在了")
	}
}

type streamEvent struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Event           json.RawMessage `json:"event"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Result          string          `json:"result"`
	Model           string          `json:"model"`
	PermissionMode  string          `json:"permissionMode"`
	IsError         bool            `json:"is_error"`
	TotalCostUSD    float64         `json:"total_cost_usd"`
	DurationMS      int64           `json:"duration_ms"`
}

func (d *claudeDriver) handleLine(line []byte) {
	var ev streamEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeen = time.Now()

	switch ev.Type {
	case "system":
		switch ev.Subtype {
		case "status": // 压缩开始时 CLI 报 status=compacting
			var st struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(line, &st) == nil && st.Status == "compacting" {
				c.setActivityLocked("compacting")
			}
			return
		case "compact_boundary":
			d.compacted(line)
			return
		case "session_state_changed":
			// running / idle / requires_action。页面发的消息自己记着轮数；这里要的是 CLI 自己开的那一轮
			var st struct {
				State string `json:"state"`
			}
			if json.Unmarshal(line, &st) == nil {
				d.sawState = true
				switch st.State {
				case "running":
					d.autoTurnLocked()
				case "idle":
					d.idleLocked()
				}
			}
			return
		case "background_tasks_changed":
			var bt struct {
				Tasks []struct {
					ID          string `json:"task_id"`
					Type        string `json:"task_type"`
					Description string `json:"description"`
				} `json:"tasks"`
			}
			if json.Unmarshal(line, &bt) == nil {
				tasks := []BgTask{}
				for _, t := range bt.Tasks {
					tasks = append(tasks, BgTask{ID: t.ID, Kind: t.Type, Description: oneLine(t.Description, 120)})
				}
				c.setBackgroundLocked(tasks)
			}
			return
		}
		// 每一轮开始都有一条 init，报当前会话、实际模型和权限模式。
		// 权限模式会被 claude 自己改掉（比如批准计划后退出 plan），以它为准
		if ev.Subtype != "init" {
			return
		}
		d.ticks++
		d.autoTurnLocked() // 老版本 CLI 不报 session_state_changed，每一轮开头的 init 也能说明它开始干活了
		changed := false
		if ev.Model != "" && ev.Model != c.resolvedModel {
			c.actualModel = "" // 换了模型（比如 /model），上一条回复的模型不作数了
		}
		for _, f := range []struct {
			dst *string
			v   string
		}{
			{&c.sessionID, ev.SessionID}, {&c.resolvedModel, ev.Model}, {&c.mode, ev.PermissionMode},
		} {
			if f.v != "" && f.v != *f.dst {
				*f.dst, changed = f.v, true
			}
		}
		if changed {
			c.broadcastStateLocked()
		}

	case "control_response":
		var resp struct {
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &resp) == nil {
			if ch, ok := d.ctrl[resp.Response.RequestID]; ok {
				ch <- ctrlResult{ok: resp.Response.Subtype == "success", data: resp.Response.Response, err: resp.Response.Error}
			}
		}

	case "stream_event":
		// 逐字流式只推不存：完整的 assistant 记录随后就到，快照里有它就够了
		if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
			return
		}
		var se struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if json.Unmarshal(ev.Event, &se) != nil {
			return
		}
		switch se.Type {
		case "content_block_delta":
			if se.Delta.Type == "text_delta" {
				c.broadcastLocked(map[string]any{"type": "delta", "text": se.Delta.Text})
			}
		case "message_start":
			d.ticks++
			c.setActivityLocked("thinking") // 新一轮 API 调用（工具跑完回来也是这样）
		case "content_block_start":
			switch se.ContentBlock.Type {
			case "thinking", "redacted_thinking":
				c.setActivityLocked("thinking")
			case "text":
				c.setActivityLocked("text")
			case "tool_use", "server_tool_use":
				c.setActivityLocked("tool:" + se.ContentBlock.Name)
			}
		}

	case "assistant", "user":
		var r record
		if json.Unmarshal(line, &r) != nil {
			return
		}
		if e := entryFromRecord(&r); e != nil {
			c.addEntryLocked(*e)
			if e.Model != "" && e.Model != c.actualModel {
				c.actualModel = e.Model
				c.broadcastStateLocked()
			}
		}

	case "control_request":
		var req struct {
			Subtype     string          `json:"subtype"`
			ToolName    string          `json:"tool_name"`
			Input       json.RawMessage `json:"input"`
			Description string          `json:"description"`
			BlockedPath string          `json:"blocked_path"`
			Suggestions json.RawMessage `json:"permission_suggestions"`
		}
		_ = json.Unmarshal(ev.Request, &req)
		if req.Subtype != "can_use_tool" {
			// 没配 MCP / hook 回调时不会有别的请求；真来了也要回一句，不然 claude 会一直等
			go c.write(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "error", "request_id": ev.RequestID, "error": "rcweb 不支持 " + req.Subtype,
			}})
			return
		}
		sugg := strings.TrimSpace(string(req.Suggestions))
		p := &PermissionReq{
			ID: ev.RequestID, Tool: req.ToolName, Input: clipInput(req.Input),
			Description: req.Description, BlockedPath: req.BlockedPath,
			CanAlways: sugg != "" && sugg != "null" && sugg != "[]",
			Always:    describeSuggestions(req.Suggestions),
			rawInput:  req.Input, suggestions: req.Suggestions,
		}
		p.answer = d.answer(p)
		c.addPendingLocked(p)

	case "prompt_suggestion":
		var ps struct {
			Suggestion string `json:"suggestion"`
		}
		if json.Unmarshal(line, &ps) == nil && c.turns == 0 {
			c.suggestion = strings.TrimSpace(ps.Suggestion)
			c.broadcastLocked(map[string]any{"type": "suggestion", "text": c.suggestion})
		}

	case "rate_limit_event":
		noteRateLimit(line) // 5h / 7d 额度，见 usage.go

	case "compact_progress":
		// 压缩的生命周期：hooks_start → compact_start → compact_end，页面上据此显示进度条
		var cp struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		if json.Unmarshal(line, &cp) == nil {
			switch cp.Event.Type {
			case "hooks_start", "compact_start":
				c.setActivityLocked("compacting")
			case "compact_end":
				c.setActivityLocked("thinking")
			}
		}

	case "control_cancel_request":
		c.removePendingLocked(ev.RequestID)

	case "result":
		switch {
		case ev.Subtype == "success":
			c.outcome = "ok"
		case ev.Subtype == "error_during_execution":
			c.outcome = "interrupted"
		default:
			c.outcome = "error"
		}
		c.unread = true
		c.activity = ""
		switch {
		case ev.Subtype == "error_during_execution":
			c.turns, c.autoTurn = 0, false // 被中断：排队的也一起作废
		case c.autoTurn:
			c.autoTurn = false // 结束的是 CLI 自己开的那一轮；这期间页面发的消息还排着
		case c.turns > 0:
			c.turns--
		}
		note := ""
		if ev.Subtype != "success" {
			note = "已中断"
			if ev.Subtype != "error_during_execution" {
				note = "出错：" + ev.Subtype
				if ev.Result != "" {
					note += " — " + oneLine(ev.Result, 200)
				}
			}
			c.addEntryLocked(noteEntry(note, ""))
		}
		c.broadcastLocked(map[string]any{
			"type": "result", "ok": !ev.IsError, "subtype": ev.Subtype,
			"cost": ev.TotalCostUSD, "durationMs": ev.DurationMS,
		})
		if d.clearing {
			// /clear 这一轮结束：claude 已经换成新会话，页面也从头开始
			d.clearing = false
			c.entries = []Entry{noteEntry("上下文已清空，下面是新会话", "")}
			c.broadcastLocked(c.snapshotLocked())
		}
		c.setStateLocked()
		go d.refreshContext()  // 这一轮结束，上下文用量变了
		go d.refreshSettings() // 这一轮里可能跑过 /model、/effort
		// 状态从 running 回到 idle 时 setStateLocked 已经广播过；结果（done / error）这里再报一次
		c.broadcastStateLocked()
		c.afterTurnLocked(note)
		if c.turns > 0 && !d.sawState {
			go d.settle(d.ticks)
		}
	}
}

// idleLocked：CLI 说它闲下来了 —— 没有在跑的，也没有排着的。页面记的轮数以它为准：
// 它干活时（比如在跑工具）收到的消息会被并进当前这一轮，一起只回一个 result，光数 result 会一直停在「运行中」
func (d *claudeDriver) idleLocked() {
	c := d.c
	if c.turns == 0 && !c.autoTurn {
		return
	}
	c.turns, c.autoTurn = 0, false
	c.setStateLocked()
	c.afterTurnLocked("") // 刚才那个 result 时还算「运行中」没报的完成，现在补上
}

var settleDelay = 3 * time.Second

// settle：老版本 CLI 不报 session_state_changed。result 之后页面还记着有消息没回，就等几秒：
// CLI 真有排着的会马上开下一轮（init），一直没动静就是被并进刚才那一轮了
func (d *claudeDriver) settle(seen int) {
	select {
	case <-time.After(settleDelay):
	case <-d.c.done:
		return
	}
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if d.ticks == seen && c.state == "running" {
		d.idleLocked()
	}
}

// autoTurnLocked：CLI 开始干活了。没有页面发出去、还没结束的消息，就是它自己开的一轮
// （后台命令 / 子代理跑完，它接着处理结果）：页面上也要显示成运行中，而不是停在「已完成」
func (d *claudeDriver) autoTurnLocked() {
	c := d.c
	if c.turns > 0 || c.autoTurn || c.state == "exited" {
		return
	}
	c.autoTurn = true
	c.outcome, c.unread, c.suggestion = "", false, ""
	c.setActivityLocked("thinking")
	c.setStateLocked()
}

// refreshSettings 用 get_settings 问 CLI 实际生效的模型全名和 effort（应答里的 applied，本地算、不发请求）。
// 没指定 effort 时页面上也能显示具体的一档，而不是「默认」—— 默认档随模型变（Opus 5.5 是 medium、Sonnet 5 是 high），
// 也受 settings.json 的 effortLevel / modelSettings 影响，只有 CLI 自己算得准。不支持 effort 的模型（Haiku）是 null
func (d *claudeDriver) refreshSettings() {
	raw, err := d.control(map[string]any{"subtype": "get_settings"}, 15*time.Second)
	if err != nil {
		return
	}
	model, effort, ok := parseApplied(raw)
	if !ok {
		return
	}
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	if model != "" && model != c.resolvedModel {
		c.resolvedModel, changed = model, true
	}
	if effort != c.effort {
		c.effort, changed = effort, true
	}
	if changed {
		c.broadcastStateLocked()
	}
}

// parseApplied 取 get_settings 应答里的 applied：{model, effort}。老版本 CLI 没有这一项，ok=false
func parseApplied(raw json.RawMessage) (model, effort string, ok bool) {
	var v struct {
		Applied *struct {
			Model  string `json:"model"`
			Effort any    `json:"effort"` // 字符串，或 null（模型不支持 effort）
		} `json:"applied"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Applied == nil {
		return "", "", false
	}
	effort, _ = v.Applied.Effort.(string)
	return v.Applied.Model, effort, true
}

// refreshContext 用 get_context_usage 问一下上下文用了多少（CLI 本地算，不花 token，和 /context 的数一致），
// 只留页面用得着的几项，广播出去、也放进快照
func (d *claudeDriver) refreshContext() {
	raw, err := d.control(map[string]any{"subtype": "get_context_usage"}, 15*time.Second)
	if err != nil {
		return
	}
	var full map[string]json.RawMessage
	if json.Unmarshal(raw, &full) != nil {
		return
	}
	keep := map[string]json.RawMessage{}
	for _, k := range []string{"totalTokens", "maxTokens", "percentage", "autoCompactThreshold", "isAutoCompactEnabled", "model"} {
		if v, ok := full[k]; ok {
			keep[k] = v
		}
	}
	b, _ := json.Marshal(keep)
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context = b
	c.broadcastLocked(map[string]any{"type": "context", "context": json.RawMessage(b)})
}

// compacted：system/compact_boundary，压缩完成。留一行「已压缩：120k → 18k tokens · 12s」
func (d *claudeDriver) compacted(line []byte) {
	var ev struct {
		Meta struct {
			Trigger    string `json:"trigger"`
			PreTokens  int    `json:"pre_tokens"`
			PostTokens int    `json:"post_tokens"`
			DurationMS int64  `json:"duration_ms"`
		} `json:"compact_metadata"`
	}
	_ = json.Unmarshal(line, &ev)
	c := d.c
	text := "上下文已压缩"
	if ev.Meta.Trigger == "auto" {
		text = "上下文快满了，已自动压缩"
	}
	if ev.Meta.PreTokens > 0 {
		text += "：" + fmtTokens(ev.Meta.PreTokens)
		if ev.Meta.PostTokens > 0 {
			text += " → " + fmtTokens(ev.Meta.PostTokens)
		}
		text += " tokens"
	}
	if ev.Meta.DurationMS > 0 {
		text += fmt.Sprintf(" · %.0fs", float64(ev.Meta.DurationMS)/1000)
	}
	c.addEntryLocked(noteEntry(text, ""))
	c.setActivityLocked("thinking")
	go d.refreshContext()
}

func fmtTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
