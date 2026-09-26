package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// grok：`grok agent stdio` 常驻进程，说 ACP（Agent Client Protocol，Zed 那套 JSON-RPC）。
//
//	initialize → session/new（或 session/resume：续会话但不重放历史，历史已经从 updates.jsonl 读好了）
//	→ 每条消息一个 session/prompt，这个请求要等整轮跑完才应答；过程中推 session/update 碎片，
//	要用工具时反过来发 session/request_permission。
//
// 同一个会话同时只跑一个 prompt，新消息在这里排队。权限模式只能在开会话时定（_meta.yoloMode / autoMode），
// 之后能切的只有 always-approve 开关，用它自己的 /always-approve on|off 命令。

type grokDriver struct {
	c         *Chat
	cfg       *Config
	rpc       *rpcConn
	onCatalog func(json.RawMessage) json.RawMessage

	// 以下由 c.mu 保护
	ready        bool
	sid          string
	busy         bool
	queue        []string
	started      time.Time
	acp          *acpBuilder
	models       []grokModel
	commands     []map[string]string
	defaultModel string                     // 开会话时的模型：选回 default 时切回它
	perms        map[string]json.RawMessage // 页面上的权限请求 ID → ACP 请求 ID：中断时要逐个回 cancelled
}

type grokModelState struct {
	CurrentModelID  string      `json:"currentModelId"`
	AvailableModels []grokModel `json:"availableModels"`
}

type grokModel struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Meta        struct {
		Efforts []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"reasoningEfforts"`
		Effort string `json:"reasoningEffort"` // 这个模型现在配置的那一档
	} `json:"_meta"`
}

func newGrokDriver(c *Chat, cfg *Config, onCatalog func(json.RawMessage) json.RawMessage) *grokDriver {
	d := &grokDriver{c: c, cfg: cfg, onCatalog: onCatalog, perms: map[string]json.RawMessage{}}
	d.rpc = newRPC("grok", c.write, c.done, true)
	// 页面上记过的用户消息不用 grok 再回一遍；正文碎片推给页面做流式显示
	d.acp = newACPBuilder(func(e Entry) {
		if e.Role != "user" || e.Blocks[0].T != "text" {
			c.addEntryLocked(e)
		}
	}, func(text string) { c.broadcastLocked(map[string]any{"type": "delta", "text": text}) })
	return d
}

func grokArgs(model, effort string) []string {
	// --no-leader：不接到共享的 leader 进程上，进程归这个对话独占，结束对话就能整组收掉
	args := []string{"agent", "--no-leader"}
	if model != "" && model != "default" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}
	return append(args, "stdio")
}

func (d *grokDriver) command(o StartOpts) *exec.Cmd {
	cmd := exec.Command(d.cfg.GrokBin, grokArgs(o.Model, o.Effort)...)
	cmd.Dir = o.cwd
	cmd.Env = childEnv()
	return cmd
}

func grokInitParams() map[string]any {
	// 不认领文件系统和终端能力：让 grok 用它自己的工具读写文件、跑命令
	return map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "rcweb", "version": "1.0"},
	}
}

func (d *grokDriver) begin(o StartOpts) {
	c := d.c
	go func() {
		raw, err := d.rpc.call("initialize", grokInitParams(), time.Minute)
		if err != nil {
			c.fail("grok 启动失败", err)
			return
		}
		var init struct {
			Meta struct {
				ModelState grokModelState `json:"modelState"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(raw, &init)
		c.mu.Lock()
		d.setModelsLocked(init.Meta.ModelState)
		c.mu.Unlock()

		params := map[string]any{"cwd": o.cwd, "mcpServers": []any{}, "_meta": map[string]bool{
			"yoloMode": o.PermissionMode == "bypassPermissions", "autoMode": o.PermissionMode == "auto",
		}}
		method := "session/new"
		if o.SessionID != "" {
			method, params["sessionId"] = "session/resume", o.SessionID
		}
		// 开会话要连 MCP、扫仓库，慢的时候要几十秒
		raw, err = d.rpc.call(method, params, 3*time.Minute)
		if err != nil {
			c.fail("grok 开会话失败", err)
			return
		}
		var resp struct {
			SessionID     string         `json:"sessionId"`
			Models        grokModelState `json:"models"`
			ConfigOptions []struct {
				ID           string `json:"id"`
				CurrentValue string `json:"currentValue"`
			} `json:"configOptions"`
		}
		_ = json.Unmarshal(raw, &resp)
		c.mu.Lock()
		d.sid = o.SessionID
		if resp.SessionID != "" {
			d.sid = resp.SessionID
		}
		d.ready = true
		c.sessionID = d.sid
		if len(resp.Models.AvailableModels) > 0 {
			d.setModelsLocked(resp.Models)
		}
		for _, opt := range resp.ConfigOptions {
			if opt.ID == "reasoning_effort" && c.effort == "" {
				c.effort = opt.CurrentValue
			}
		}
		c.broadcastStateLocked()
		c.mu.Unlock()
		d.pump()
	}()
}

func (d *grokDriver) setModelsLocked(st grokModelState) {
	if len(st.AvailableModels) == 0 {
		return
	}
	d.models = st.AvailableModels
	if d.defaultModel == "" {
		d.defaultModel = st.CurrentModelID
	}
	if st.CurrentModelID != "" {
		d.c.resolvedModel = st.CurrentModelID
	}
	d.publishCatalogLocked()
}

func (d *grokDriver) publishCatalogLocked() {
	raw := grokCatalog(d.models, d.defaultModel, d.commands)
	if bytes.Equal(raw, d.c.catalog) { // 命令表会重复推好几遍，没变就不打扰页面
		return
	}
	d.c.setCatalogLocked(raw)
	go d.onCatalog(raw)
}

// grokCatalog 拼成和 claude 的 initialize 应答同样的形状，页面上的模型 / effort 选择器、斜杠菜单通用
func grokCatalog(models []grokModel, current string, commands []map[string]string) json.RawMessage {
	out := []catalogModel{}
	for _, m := range models {
		e := []string{}
		def := m.Meta.Effort
		for _, x := range m.Meta.Efforts {
			e = append(e, x.ID)
			if def == "" && x.Default {
				def = x.ID
			}
		}
		one := catalogModel{Value: m.ModelID, DisplayName: m.Name, Description: m.Description, Resolved: m.ModelID, Efforts: e, DefaultEffort: def}
		if m.ModelID == current {
			// 名字写具体的模型，页面上不显示「Default」
			d := one
			d.Value, d.Description = "default", "跟着 grok 的配置走"
			out = append([]catalogModel{d}, out...)
		}
		out = append(out, one)
	}
	if commands == nil {
		commands = []map[string]string{}
	}
	return mustJSON(map[string]any{"models": out, "commands": commands})
}

// grok 不收图片（initialize 报 promptCapabilities.image=false），附件只有正文里的路径
func (d *grokDriver) prompt(text string, _ []*Upload) error {
	d.c.mu.Lock()
	d.queue = append(d.queue, text)
	d.c.mu.Unlock()
	d.pump()
	return nil
}

func (d *grokDriver) pump() {
	c := d.c
	c.mu.Lock()
	if !d.ready || d.busy || len(d.queue) == 0 || c.state == "exited" {
		c.mu.Unlock()
		return
	}
	text := d.queue[0]
	d.queue = d.queue[1:]
	d.busy, d.started = true, time.Now()
	params := map[string]any{"sessionId": d.sid, "prompt": []map[string]string{{"type": "text", "text": text}}}
	c.mu.Unlock()

	go func() {
		raw, err := d.rpc.call("session/prompt", params, 0) // 整轮跑完才应答，可能几十分钟
		var resp struct {
			StopReason string `json:"stopReason"`
			Meta       struct {
				Usage struct {
					CostUsdTicks float64 `json:"costUsdTicks"`
				} `json:"usage"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(raw, &resp)
		outcome, note := "ok", ""
		switch {
		case err != nil:
			outcome, note = "error", "出错："+err.Error()
		case resp.StopReason == "cancelled":
			outcome, note = "interrupted", "已中断"
		case resp.StopReason == "max_tokens":
			outcome, note = "error", "出错：回复超出长度上限"
		case resp.StopReason == "max_turn_requests":
			outcome, note = "error", "出错：超出单轮工具调用上限"
		case resp.StopReason == "refusal":
			outcome, note = "error", "模型拒绝了这个请求"
		case resp.StopReason != "" && resp.StopReason != "end_turn":
			outcome, note = "error", "出错："+resp.StopReason
		}
		c.mu.Lock()
		d.acp.flush()
		d.busy = false
		c.endTurnLocked(outcome, note, resp.Meta.Usage.CostUsdTicks/1e10, time.Since(d.started))
		c.mu.Unlock()
		d.pump()
	}()
}

func (d *grokDriver) interrupt() error {
	c := d.c
	c.mu.Lock()
	d.queue = nil
	if !d.busy {
		c.turns = 0
		c.setStateLocked()
		c.mu.Unlock()
		return nil
	}
	// ACP 的约定：取消时客户端要把还挂着的权限请求都回成 cancelled
	for reqID, id := range d.perms {
		_ = d.rpc.reply(id, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
		c.removePendingLocked(reqID)
	}
	d.perms = map[string]json.RawMessage{}
	sid := d.sid
	c.mu.Unlock()
	return d.rpc.notify("session/cancel", map[string]string{"sessionId": sid})
}

func (d *grokDriver) session() (string, error) {
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	if !d.ready {
		return "", errors.New("grok 还没准备好，稍等一下")
	}
	return d.sid, nil
}

func (d *grokDriver) setModel(model string) error {
	sid, err := d.session()
	if err != nil {
		return err
	}
	c := d.c
	id := model
	if model == "default" {
		c.mu.Lock()
		id = d.defaultModel
		c.mu.Unlock()
	}
	if _, err := d.rpc.call("session/set_config_option", map[string]any{"sessionId": sid, "configId": "model", "value": id}, 30*time.Second); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model, c.resolvedModel = model, id
	c.broadcastStateLocked()
	return nil
}

// setEffort 只把请求写出去就返回（WebSocket 读循环同步调它），应答在后台等
func (d *grokDriver) setEffort(effort string) error {
	sid, err := d.session()
	if err != nil {
		return err
	}
	ch, err := d.rpc.start("session/set_config_option", map[string]any{"sessionId": sid, "configId": "reasoning_effort", "value": effort})
	if err != nil {
		return err
	}
	c := d.c
	c.mu.Lock()
	c.setLocked(&c.effort, effort)
	c.mu.Unlock()
	go func() {
		if _, err := d.rpc.wait(ch, 30*time.Second); err != nil {
			c.mu.Lock()
			c.addStderrLocked("改 effort 失败：" + err.Error())
			c.mu.Unlock()
		}
	}()
	return nil
}

func (d *grokDriver) setMode(mode string) error {
	c := d.c
	c.mu.Lock()
	cur := c.mode
	c.mu.Unlock()
	if mode == "auto" || cur == "auto" {
		return errors.New("grok 的 auto 模式只能在开新对话时选")
	}
	cmd := "/always-approve off"
	if mode == "bypassPermissions" {
		cmd = "/always-approve on"
	}
	if err := c.send(cmd, nil, false); err != nil {
		return err
	}
	c.mu.Lock()
	c.setLocked(&c.mode, mode)
	c.mu.Unlock()
	return nil
}

func (d *grokDriver) handleLine(line []byte) {
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

	var p struct {
		Update json.RawMessage `json:"update"`
	}
	_ = json.Unmarshal(m.Params, &p)
	switch m.Method {
	case "session/update":
		if a := d.acp.update(p.Update, nowTS()); a != "" {
			c.setActivityLocked(a)
		}
		var u struct {
			SessionUpdate string `json:"sessionUpdate"`
			Title         string `json:"title"`
			ConfigOptions []struct {
				ID           string `json:"id"`
				CurrentValue string `json:"currentValue"`
			} `json:"configOptions"`
			Commands []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Input       *struct {
					Hint string `json:"hint"`
				} `json:"input"`
			} `json:"availableCommands"`
		}
		_ = json.Unmarshal(p.Update, &u)
		switch u.SessionUpdate {
		case "session_info_update":
			if u.Title != "" {
				c.setLocked(&c.title, oneLine(u.Title, 60))
			}
		case "config_option_update":
			for _, opt := range u.ConfigOptions {
				switch opt.ID {
				case "model":
					c.setLocked(&c.resolvedModel, opt.CurrentValue)
				case "reasoning_effort":
					c.setLocked(&c.effort, opt.CurrentValue)
				}
			}
		case "available_commands_update":
			d.commands = []map[string]string{}
			for _, cmd := range u.Commands {
				item := map[string]string{"name": cmd.Name, "description": cmd.Description}
				if cmd.Input != nil && cmd.Input.Hint != "" {
					item["argumentHint"] = cmd.Input.Hint
				}
				d.commands = append(d.commands, item)
			}
			d.publishCatalogLocked()
		}

	case "_x.ai/session_notification", "_x.ai/session/update":
		var u struct {
			SessionUpdate string `json:"sessionUpdate"`
			Attempt       int    `json:"attempt"`
			MaxRetries    int    `json:"max_retries"`
			Reason        string `json:"reason"`
		}
		_ = json.Unmarshal(p.Update, &u)
		if u.SessionUpdate == "retry_state" && u.Reason != "" {
			c.addStderrLocked(fmt.Sprintf("重试 %d/%d：%s", u.Attempt, u.MaxRetries, u.Reason))
		}
		d.acp.extension(p.Update, nowTS())
	}
}

func (d *grokDriver) handleRequest(m *rpcMsg) {
	c := d.c
	id := m.ID
	if m.Method != "session/request_permission" {
		go d.rpc.replyErr(id, -32601, "rcweb 不支持 "+m.Method)
		return
	}
	var p struct {
		ToolCall acpUpdate `json:"toolCall"`
		Options  []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"` // allow_once / allow_always / reject_once / reject_always
		} `json:"options"`
	}
	_ = json.Unmarshal(m.Params, &p)
	option := map[string]string{}
	for _, o := range p.Options {
		if option[o.Kind] == "" {
			option[o.Kind] = o.OptionID
		}
	}
	name := p.ToolCall.Meta.Tool.Name
	if name == "" {
		name = p.ToolCall.Title
	}
	tool, input := grokTool(name, p.ToolCall.RawInput)
	reqID := "grok-" + string(id)
	d.perms[reqID] = id
	c.addPendingLocked(&PermissionReq{
		ID: reqID, Tool: tool, Input: input, Description: p.ToolCall.Title, CanAlways: option["allow_always"] != "",
		answer: func(a PermissionAnswer) error {
			c.mu.Lock()
			delete(d.perms, reqID)
			c.mu.Unlock()
			pick := map[string][]string{
				"allow":  {"allow_once", "allow_always"},
				"always": {"allow_always", "allow_once"},
				"deny":   {"reject_once", "reject_always"},
			}[a.Choice]
			for _, kind := range pick {
				if option[kind] != "" {
					return d.rpc.reply(id, map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option[kind]}})
				}
			}
			return d.rpc.reply(id, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
		},
	})
}

// fetchGrokCatalog：新对话还没开始时，起一个短命的 grok agent 问一遍模型列表（initialize 的应答里就有，不用开会话）
func fetchGrokCatalog(bin, cwd string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := quickRPC("grok", exec.Command(bin, grokArgs("", "")...), cwd, true, func(r *rpcConn) error {
		resp, err := r.call("initialize", grokInitParams(), time.Minute)
		if err != nil {
			return err
		}
		var init struct {
			Meta struct {
				ModelState grokModelState `json:"modelState"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(resp, &init)
		if len(init.Meta.ModelState.AvailableModels) == 0 {
			return errors.New("grok 没有返回可用模型")
		}
		raw = grokCatalog(init.Meta.ModelState.AvailableModels, init.Meta.ModelState.CurrentModelID, nil)
		return nil
	})
	return raw, err
}
