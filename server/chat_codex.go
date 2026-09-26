package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// codex：`codex app-server` 常驻进程，JSON-RPC over stdio（和 VS Code 插件用的是同一个协议）。
//
//	initialize → thread/start（或 thread/resume）→ 每条消息一个 turn/start
//	过程中推 item/started、item/agentMessage/delta、item/completed、turn/completed 等通知；
//	要跑命令、改文件时反过来发 item/*/requestApproval 请求，等页面上批。
//
// 一个 turn 跑着的时候不再发 turn/start，新消息在这里排队，上一轮结束再发（claude 那边是 CLI 自己排队）。
// 模型、effort、权限模式在 codex 里是「下一个 turn 起生效」的参数，改了先记着，随下一条消息带过去。

type codexDriver struct {
	c         *Chat
	cfg       *Config
	rpc       *rpcConn
	onCatalog func(json.RawMessage) json.RawMessage

	// 以下由 c.mu 保护
	ready   bool               // 握手完、线程开好了
	thread  string             // 线程 ID（= 会话 ID）
	turn    string             // 正在跑的 turn
	busy    bool               // 有 turn 在跑（包括 turn/start 已发出、还没应答的）
	queue   [][]map[string]any // 排着的消息，每条是 turn/start 的 input
	next    map[string]any     // 下一个 turn/start 要带的设置变更
	items   map[string]Block   // 已经推给页面、还没结束的工具调用：审批卡片要用它的参数
	started time.Time
	models  []codexModel
}

type codexModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
	Hidden      bool   `json:"hidden"`
	Efforts     []struct {
		Effort string `json:"reasoningEffort"`
	} `json:"supportedReasoningEfforts"`
	DefaultEffort string `json:"defaultReasoningEffort"`
}

func newCodexDriver(c *Chat, cfg *Config, onCatalog func(json.RawMessage) json.RawMessage) *codexDriver {
	d := &codexDriver{c: c, cfg: cfg, onCatalog: onCatalog, next: map[string]any{}, items: map[string]Block{}}
	d.rpc = newRPC("codex", c.write, c.done, false)
	return d
}

func (d *codexDriver) command(o StartOpts) *exec.Cmd {
	cmd := exec.Command(d.cfg.CodexBin, "app-server")
	cmd.Dir = o.cwd
	cmd.Env = childEnv()
	return cmd
}

func (d *codexDriver) begin(o StartOpts) {
	c := d.c
	go func() {
		if _, err := d.rpc.call("initialize", codexClientInfo(), 30*time.Second); err != nil {
			c.fail("codex 启动失败", err)
			return
		}
		_ = d.rpc.notify("initialized", map[string]any{})
		go d.loadModels()

		mode := codexModes[o.PermissionMode]
		params := map[string]any{"cwd": o.cwd, "approvalPolicy": mode.approval, "sandbox": mode.sandbox}
		if o.Model != "" && o.Model != "default" {
			params["model"] = o.Model
		}
		method := "thread/start"
		if o.SessionID != "" {
			// 历史已经从 rollout 读好了（handleStartChat），不用它再回一遍
			method, params["threadId"], params["excludeTurns"] = "thread/resume", o.SessionID, true
		}
		raw, err := d.rpc.call(method, params, 2*time.Minute)
		if err != nil {
			c.fail("codex 开会话失败", err)
			return
		}
		var resp struct {
			Thread struct {
				ID   string  `json:"id"`
				Name *string `json:"name"`
			} `json:"thread"`
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoningEffort"`
		}
		_ = json.Unmarshal(raw, &resp)
		c.mu.Lock()
		d.thread, d.ready = resp.Thread.ID, true
		c.sessionID, c.resolvedModel = resp.Thread.ID, resp.Model
		if c.effort == "" {
			c.effort = resp.ReasoningEffort
		} else {
			d.next["effort"] = c.effort // thread/start 没有 effort 参数，随第一个 turn 带过去
		}
		if resp.Thread.Name != nil && *resp.Thread.Name != "" && c.title == "" {
			c.title = oneLine(*resp.Thread.Name, 60)
		}
		c.broadcastStateLocked()
		c.mu.Unlock()
		d.pump()
	}()
}

func codexClientInfo() map[string]any {
	return map[string]any{"clientInfo": map[string]any{"name": "rcweb", "title": "rcweb", "version": "1.0"}, "capabilities": nil}
}

func (d *codexDriver) loadModels() {
	raw, err := d.rpc.call("model/list", map[string]any{}, 30*time.Second)
	if err != nil {
		return
	}
	models := parseCodexModels(raw)
	cat := codexCatalog(models)
	d.c.mu.Lock()
	d.models = models
	d.c.setCatalogLocked(cat)
	d.c.mu.Unlock()
	d.onCatalog(cat)
}

func parseCodexModels(raw json.RawMessage) []codexModel {
	var resp struct {
		Data []codexModel `json:"data"`
	}
	_ = json.Unmarshal(raw, &resp)
	return resp.Data
}

// codexCatalog 拼成和 claude 的 initialize 应答同样的形状，页面上的模型 / effort 选择器通用
func codexCatalog(models []codexModel) json.RawMessage {
	out := []catalogModel{}
	for _, m := range models {
		if m.Hidden {
			continue
		}
		e := []string{}
		for _, x := range m.Efforts {
			e = append(e, x.Effort)
		}
		one := catalogModel{Value: m.ID, DisplayName: m.DisplayName, Description: m.Description, Resolved: m.ID, Efforts: e, DefaultEffort: m.DefaultEffort}
		if m.IsDefault {
			// 名字写具体的模型，页面上不显示「Default」
			d := one
			d.Value, d.Description = "default", "跟着 codex 的配置走"
			out = append([]catalogModel{d}, out...)
		}
		out = append(out, one)
	}
	return mustJSON(map[string]any{"models": out, "commands": []any{}})
}

// 图片用 localImage 直接给模型看（codex 自己读文件、自己缩），放在文字前面
func (d *codexDriver) prompt(text string, atts []*Upload) error {
	var input []map[string]any
	for _, u := range atts {
		if u.Image {
			input = append(input, map[string]any{"type": "localImage", "path": u.vision})
		}
	}
	input = append(input, map[string]any{"type": "text", "text": text, "text_elements": []any{}})
	d.c.mu.Lock()
	d.queue = append(d.queue, input)
	d.c.mu.Unlock()
	d.pump()
	return nil
}

// pump：空闲又有排队的消息就发下一条
func (d *codexDriver) pump() {
	c := d.c
	c.mu.Lock()
	if !d.ready || d.busy || len(d.queue) == 0 || c.state == "exited" {
		c.mu.Unlock()
		return
	}
	input := d.queue[0]
	d.queue = d.queue[1:]
	d.busy, d.turn, d.started = true, "", time.Now()
	params := map[string]any{"threadId": d.thread, "input": input}
	for k, v := range d.next {
		params[k] = v
	}
	d.next = map[string]any{}
	c.mu.Unlock()

	go func() {
		raw, err := d.rpc.call("turn/start", params, time.Minute)
		c.mu.Lock()
		if err != nil {
			d.busy = false
			c.endTurnLocked("error", "发送失败："+err.Error(), 0, 0)
			c.mu.Unlock()
			d.pump()
			return
		}
		var resp struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &resp) == nil && d.busy && d.turn == "" {
			d.turn = resp.Turn.ID
		}
		c.mu.Unlock()
	}()
}

func (d *codexDriver) interrupt() error {
	c := d.c
	c.mu.Lock()
	d.queue = nil
	if !d.busy {
		c.turns = 0
		c.setStateLocked()
		c.mu.Unlock()
		return nil
	}
	thread, turn := d.thread, d.turn
	c.mu.Unlock()
	if turn == "" {
		return errors.New("这一轮还没开始，稍后再试")
	}
	_, err := d.rpc.call("turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, 15*time.Second)
	return err
}

// 模型 / effort / 权限模式：codex 里都是「这个 turn 及以后」的参数，记下来随下一条消息带过去
func (d *codexDriver) setModel(model string) error {
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	id := model
	if model == "default" {
		id = ""
		for _, m := range d.models {
			if m.IsDefault {
				id = m.ID
			}
		}
		if id == "" {
			return errors.New("还不知道 codex 的默认模型，直接选一个具体的")
		}
	}
	d.next["model"] = id
	c.model, c.resolvedModel = model, id
	c.broadcastStateLocked()
	return nil
}

func (d *codexDriver) setEffort(effort string) error {
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	d.next["effort"] = effort
	c.setLocked(&c.effort, effort)
	return nil
}

func (d *codexDriver) setMode(mode string) error {
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	m := codexModes[mode]
	d.next["approvalPolicy"], d.next["sandboxPolicy"] = m.approval, m.sandboxPolicy()
	c.setLocked(&c.mode, mode)
	return nil
}

func (d *codexDriver) handleLine(line []byte) {
	m := d.rpc.dispatch(line)
	if m == nil {
		return
	}
	c := d.c
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeen = time.Now()
	if m.isRequest() {
		d.handleRequest(m)
		return
	}

	switch m.Method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(m.Params, &p) == nil && d.busy {
			d.turn = p.Turn.ID
		}

	case "turn/completed":
		var p struct {
			Turn struct {
				Status     string `json:"status"`
				DurationMS int64  `json:"durationMs"`
				Error      *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(m.Params, &p)
		outcome, note := "ok", ""
		switch p.Turn.Status {
		case "interrupted":
			outcome, note = "interrupted", "已中断"
		case "failed":
			outcome, note = "error", "出错"
			if p.Turn.Error != nil && p.Turn.Error.Message != "" {
				note += "：" + oneLine(p.Turn.Error.Message, 300)
			}
		}
		dur := time.Duration(p.Turn.DurationMS) * time.Millisecond
		if dur == 0 {
			dur = time.Since(d.started)
		}
		d.busy, d.turn = false, ""
		d.items = map[string]Block{}
		c.endTurnLocked(outcome, note, 0, dur)
		go d.pump()

	case "item/started":
		var p struct {
			Item json.RawMessage `json:"item"`
		}
		_ = json.Unmarshal(m.Params, &p)
		it, ok := parseCodexItem(p.Item)
		switch {
		case !ok:
		case it.isTool():
			b := it.toolUse()
			d.items[it.ID] = b
			c.addEntryLocked(Entry{Role: "assistant", Blocks: []Block{b}, TS: nowTS()})
			c.setActivityLocked("tool:" + b.Name)
		case it.Type == "agentMessage":
			c.setActivityLocked("text")
		case it.Type == "reasoning":
			c.setActivityLocked("thinking")
		}

	case "item/completed":
		var p struct {
			Item json.RawMessage `json:"item"`
		}
		_ = json.Unmarshal(m.Params, &p)
		it, ok := parseCodexItem(p.Item)
		if !ok || it.Type == "userMessage" { // 用户消息页面上已经记过了
			return
		}
		if it.isTool() {
			if _, started := d.items[it.ID]; !started {
				c.addEntryLocked(Entry{Role: "assistant", Blocks: []Block{it.toolUse()}, TS: nowTS()})
			}
			delete(d.items, it.ID)
			c.addEntryLocked(Entry{Role: "user", Blocks: []Block{it.toolResult()}, TS: nowTS()})
			c.setActivityLocked("thinking")
			return
		}
		for _, e := range it.entries(nowTS()) {
			c.addEntryLocked(e)
		}

	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Delta != "" {
			c.broadcastLocked(map[string]any{"type": "delta", "text": p.Delta})
		}

	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		c.setActivityLocked("thinking")

	case "thread/name/updated":
		var p struct {
			ThreadName string `json:"threadName"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.ThreadName != "" {
			c.setLocked(&c.title, oneLine(p.ThreadName, 60))
		}

	case "model/rerouted":
		var p struct {
			ToModel string `json:"toModel"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.ToModel != "" {
			c.setLocked(&c.resolvedModel, p.ToModel)
		}

	case "serverRequest/resolved":
		// 审批请求在别处了结了（比如中断时 codex 自己撤回），页面上的卡片也收起来
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			c.removePendingLocked("codex-" + string(p.RequestID))
		}

	case "error":
		var p struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Error.Message != "" {
			prefix := ""
			if p.WillRetry {
				prefix = "（重试中）"
			}
			c.addStderrLocked(prefix + p.Error.Message)
		}
	}
}

// handleRequest：codex 反过来问我们的（审批、提问）。能在页面上答的做成卡片，别的直接回绝，免得它一直等
func (d *codexDriver) handleRequest(m *rpcMsg) {
	c := d.c
	id := m.ID
	reqID := "codex-" + string(id)
	reply := func(v any) error { return d.rpc.reply(id, v) }

	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var p struct {
			ItemID    string            `json:"itemId"`
			Reason    string            `json:"reason"`
			Command   string            `json:"command"`
			GrantRoot string            `json:"grantRoot"`
			Decisions []json.RawMessage `json:"availableDecisions"`
		}
		_ = json.Unmarshal(m.Params, &p)
		req := &PermissionReq{ID: reqID, Description: p.Reason}
		if b, ok := d.items[p.ItemID]; ok {
			req.Tool, req.Input = b.Name, b.Input
		}
		if m.Method == "item/commandExecution/requestApproval" {
			if p.Command != "" {
				req.Tool, req.Input = "Bash", mustJSON(map[string]string{"command": commandText(mustJSON(p.Command))})
			}
		} else if p.GrantRoot != "" {
			req.BlockedPath = p.GrantRoot
		}
		if req.Tool == "" {
			req.Tool, req.Input = "Bash", mustJSON(map[string]string{})
		}
		allow, always, deny := codexDecisions(p.Decisions)
		req.CanAlways = always != nil
		req.answer = func(a PermissionAnswer) error {
			decision := allow
			switch a.Choice {
			case "always":
				decision = always
			case "deny":
				decision = deny
			}
			if decision == nil {
				decision = allow
			}
			return reply(map[string]any{"decision": decision})
		}
		c.addPendingLocked(req)

	case "item/tool/requestUserInput":
		// 提问做成和 claude 的 AskUserQuestion 一样的卡片；答案按问题文本回来，这里换回问题 ID
		var p struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		_ = json.Unmarshal(m.Params, &p)
		qs := []map[string]any{}
		for _, q := range p.Questions {
			qs = append(qs, map[string]any{"question": q.Question, "header": q.Header, "multiSelect": false, "options": q.Options})
		}
		c.addPendingLocked(&PermissionReq{
			ID: reqID, Tool: "AskUserQuestion", Input: mustJSON(map[string]any{"questions": qs}),
			answer: func(a PermissionAnswer) error {
				answers := map[string]any{}
				for _, q := range p.Questions {
					if v, ok := a.Answers[q.Question]; ok && a.Choice != "deny" {
						answers[q.ID] = map[string]any{"answers": []string{v}}
					}
				}
				return reply(map[string]any{"answers": answers})
			},
		})

	case "item/permissions/requestApproval":
		var p struct {
			Reason      string          `json:"reason"`
			Permissions json.RawMessage `json:"permissions"`
		}
		_ = json.Unmarshal(m.Params, &p)
		c.addPendingLocked(&PermissionReq{
			ID: reqID, Tool: "Permissions", Description: p.Reason, CanAlways: true,
			Input: mustJSON(map[string]any{"reason": p.Reason, "permissions": p.Permissions}),
			answer: func(a PermissionAnswer) error {
				switch a.Choice {
				case "deny":
					return reply(map[string]any{"permissions": map[string]any{}, "scope": "turn"})
				case "always":
					return reply(map[string]any{"permissions": p.Permissions, "scope": "session"})
				}
				return reply(map[string]any{"permissions": p.Permissions, "scope": "turn"})
			},
		})

	case "mcpServer/elicitation/request":
		go reply(map[string]any{"action": "decline", "content": nil, "_meta": nil})

	default:
		go d.rpc.replyErr(id, -32601, "rcweb 不支持 "+m.Method)
	}
}

// codexDecisions 从 availableDecisions 里挑出「允许 / 总是允许 / 拒绝」各用哪个。
// 「总是允许」优先本会话有效的 acceptForSession，没有就用 codex 建议的执行策略规则；
// 「拒绝」优先 decline（告诉模型、这一轮接着跑），没有才用 cancel（连这一轮一起停）
func codexDecisions(avail []json.RawMessage) (allow, always, deny any) {
	if len(avail) == 0 {
		return "accept", "acceptForSession", "decline"
	}
	var cancel any
	for _, raw := range avail {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			switch s {
			case "accept":
				allow = s
			case "acceptForSession":
				always = s
			case "decline":
				deny = s
			case "cancel":
				cancel = s
			}
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj["acceptWithExecpolicyAmendment"] != nil && always == nil {
			always = raw
		}
	}
	if allow == nil {
		allow = "accept"
	}
	if deny == nil {
		deny = cancel
	}
	if deny == nil {
		deny = "decline"
	}
	return allow, always, deny
}

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }

// fetchCodexCatalog：新对话还没开始时，起一个短命的 app-server 问一遍模型列表
func fetchCodexCatalog(bin, cwd string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := quickRPC("codex", exec.Command(bin, "app-server"), cwd, false, func(r *rpcConn) error {
		if _, err := r.call("initialize", codexClientInfo(), 30*time.Second); err != nil {
			return err
		}
		_ = r.notify("initialized", map[string]any{})
		list, err := r.call("model/list", map[string]any{}, 30*time.Second)
		if err != nil {
			return err
		}
		models := parseCodexModels(list)
		if len(models) == 0 {
			return fmt.Errorf("codex 没有返回可用模型")
		}
		raw = codexCatalog(models)
		return nil
	})
	return raw, err
}
