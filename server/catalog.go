package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 目录（catalog）= claude 对 initialize 控制请求的应答：可选模型（带各自支持的 effort 档位）、
// 这个项目下能用的斜杠命令（内置 + skills + 自定义命令，带说明）、agents。
// codex / grok 没有这个请求，由 driver 拼成同样的形状：codex 问 model/list，grok 的模型在 initialize 应答里。
// 每个模型另带 defaultEffort：不指定 effort 时实际用哪一档，页面上显示具体的一档而不是「默认」。
//
// 聊天进程起来时会自己问一遍（见各 driver 的 begin），但新对话还没开始时页面也要用到它，
// 所以这里另起一个短命的进程去问，按 CLI + 项目目录缓存。命令表和项目里的 .claude/ 有关，所以按目录分开。

const catalogTTL = 10 * time.Minute

type catalogCache struct {
	mu    sync.Mutex             // 同时只起一个进程去问
	items map[string]catalogItem // key：agent + "\x00" + 目录
}

type catalogItem struct {
	at  time.Time
	raw json.RawMessage
}

func (s *Server) storeCatalog(agent, cwd string, raw json.RawMessage) json.RawMessage {
	s.catalogs.mu.Lock()
	defer s.catalogs.mu.Unlock()
	key := agent + "\x00" + cwd
	// 聊天进程自己的 initialize 应答里没有各模型的默认 effort（只有这里的短命进程挨个问过），沿用上一份的；
	// 上一份也没有就不进缓存，免得 /api/catalog 拿这份缺了默认 effort 的顶十分钟
	if old, ok := s.catalogs.items[key]; ok {
		raw = carryDefaultEfforts(old.raw, raw)
	}
	if agent == agentClaude && !hasDefaultEfforts(raw) {
		return raw
	}
	s.catalogs.items[key] = catalogItem{at: time.Now(), raw: raw}
	return raw
}

func hasDefaultEfforts(raw json.RawMessage) bool {
	_, models, _ := catalogModels(raw)
	for _, m := range models {
		if _, ok := m["defaultEffort"]; ok {
			return true
		}
	}
	return false
}

func (s *Server) catalog(agent, cwd string) (json.RawMessage, error) {
	s.catalogs.mu.Lock()
	defer s.catalogs.mu.Unlock()
	key := agent + "\x00" + cwd
	if it, ok := s.catalogs.items[key]; ok && time.Since(it.at) < catalogTTL {
		return it.raw, nil
	}
	var raw json.RawMessage
	var err error
	switch agent {
	case agentCodex:
		raw, err = fetchCodexCatalog(s.cfg.CodexBin, cwd)
	case agentGrok:
		raw, err = fetchGrokCatalog(s.cfg.GrokBin, cwd)
	default:
		raw, err = s.fetchCatalog(cwd)
	}
	if err != nil {
		return nil, err
	}
	s.catalogs.items[key] = catalogItem{at: time.Now(), raw: raw}
	return raw, nil
}

func (s *Server) fetchCatalog(cwd string) (json.RawMessage, error) {
	cmd := exec.Command(s.claudeBin(), "--print", "--verbose",
		"--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio")
	cmd.Dir = cwd
	cmd.Env = childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// stdout 上的 control_response 按 request_id 交给在等它的那次 ask
	var mu sync.Mutex
	waiting := map[string]chan ctrlResult{}
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = eachLine(stdout, 16<<20, func(line []byte) bool {
			var v struct {
				Type     string `json:"type"`
				Response struct {
					Subtype   string          `json:"subtype"`
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
				} `json:"response"`
			}
			if json.Unmarshal(line, &v) != nil || v.Type != "control_response" {
				return true
			}
			mu.Lock()
			ch := waiting[v.Response.RequestID]
			delete(waiting, v.Response.RequestID)
			mu.Unlock()
			if ch != nil {
				ch <- ctrlResult{ok: v.Response.Subtype == "success", data: v.Response.Response}
			}
			return true
		})
	}()
	n := 0
	ask := func(req map[string]any, timeout time.Duration) (json.RawMessage, error) {
		n++
		id := fmt.Sprintf("rcweb-catalog-%d", n)
		ch := make(chan ctrlResult, 1)
		mu.Lock()
		waiting[id] = ch
		mu.Unlock()
		b, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": req})
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			return nil, err
		}
		select {
		case r := <-ch:
			if !r.ok {
				return nil, fmt.Errorf("claude 拒绝了 %v", req["subtype"])
			}
			return r.data, nil // set_model 这种成功了也没有内容
		case <-exited:
			return nil, fmt.Errorf("claude 没有答 %v 就退出了", req["subtype"])
		case <-time.After(timeout):
			return nil, fmt.Errorf("等 claude 答 %v 超时", req["subtype"])
		}
	}

	raw, err := ask(map[string]any{"subtype": "initialize"}, time.Minute)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("claude 的 initialize 应答是空的")
	}
	// 各模型不指定 effort 时用哪一档：挨个 set_model 再 get_settings。只改这个短命进程里的状态，
	// 不写 settings.json、不开会话，一个模型几毫秒到一两秒；问不出来的就不带，页面上照样能用
	deadline := time.Now().Add(15 * time.Second)
	return withDefaultEfforts(trimCatalog(raw), func(model string) (string, bool) {
		if time.Now().After(deadline) {
			return "", false
		}
		if _, err := ask(map[string]any{"subtype": "set_model", "model": model}, 10*time.Second); err != nil {
			return "", false
		}
		got, err := ask(map[string]any{"subtype": "get_settings"}, 10*time.Second)
		if err != nil {
			return "", false
		}
		_, effort, ok := parseApplied(got)
		return effort, ok
	}), nil
}

// withDefaultEfforts 给目录里每个模型填上 defaultEffort（probe 问出来的，空 = 这个模型不支持 effort）。
// 第一个就问不出来（老版本 CLI 没有 get_settings）就不再往下问；后面个别问不出来的跳过
func withDefaultEfforts(raw json.RawMessage, probe func(model string) (string, bool)) json.RawMessage {
	cat, models, ok := catalogModels(raw)
	if !ok {
		return raw
	}
	for i, m := range models {
		var value string
		if json.Unmarshal(m["value"], &value) != nil || value == "" {
			continue
		}
		effort, ok := probe(value)
		if !ok && i == 0 {
			break
		}
		if ok {
			m["defaultEffort"] = mustJSON(effort)
		}
	}
	return withModels(cat, models, raw)
}

// carryDefaultEfforts：next 里没有 defaultEffort 的模型，从 prev 里同名的那个抄过来
func carryDefaultEfforts(prev, next json.RawMessage) json.RawMessage {
	_, old, ok := catalogModels(prev)
	if !ok {
		return next
	}
	efforts := map[string]json.RawMessage{}
	for _, m := range old {
		if e, ok := m["defaultEffort"]; ok {
			efforts[string(m["value"])] = e
		}
	}
	cat, models, ok := catalogModels(next)
	if !ok || len(efforts) == 0 {
		return next
	}
	for _, m := range models {
		if _, ok := m["defaultEffort"]; !ok {
			if e, ok := efforts[string(m["value"])]; ok {
				m["defaultEffort"] = e
			}
		}
	}
	return withModels(cat, models, next)
}

// catalogModels / withModels：拆开、装回目录里的 models，别的字段原样不动
func catalogModels(raw json.RawMessage) (map[string]json.RawMessage, []map[string]json.RawMessage, bool) {
	var cat map[string]json.RawMessage
	var models []map[string]json.RawMessage
	if json.Unmarshal(raw, &cat) != nil || json.Unmarshal(cat["models"], &models) != nil || len(models) == 0 {
		return nil, nil, false
	}
	return cat, models, true
}

func withModels(cat map[string]json.RawMessage, models []map[string]json.RawMessage, fallback json.RawMessage) json.RawMessage {
	b, err := json.Marshal(models)
	if err != nil {
		return fallback
	}
	cat["models"] = b
	if b, err = json.Marshal(cat); err != nil {
		return fallback
	}
	return b
}

// catalogModel：codex / grok 拼目录时的一个模型，字段和 claude 的 initialize 应答一致
type catalogModel struct {
	Value         string   `json:"value"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description,omitempty"`
	Resolved      string   `json:"resolvedModel,omitempty"`
	Efforts       []string `json:"supportedEffortLevels"`
	DefaultEffort string   `json:"defaultEffort,omitempty"`
}

// trimCatalog 只留页面用得上的几项，账号、遥测开关之类不往浏览器发
func trimCatalog(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	out := map[string]json.RawMessage{}
	for _, k := range []string{"models", "commands", "agents", "output_style", "available_output_styles"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	agent := normAgent(r.URL.Query().Get("agent"))
	if !validAgent(agent) {
		writeErr(w, http.StatusBadRequest, "不认识的 agent")
		return
	}
	cwd := s.cfg.Root
	if p := r.URL.Query().Get("project"); p != "" {
		path, ok := s.projectPath(p)
		if !ok {
			writeErr(w, http.StatusNotFound, "没有这个项目")
			return
		}
		if isDir(path) {
			cwd = path
		}
	}
	raw, err := s.catalog(agent, cwd)
	if err != nil {
		writeErr(w, http.StatusBadGateway, strings.TrimSpace(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// quickRPC 起一个短命的 JSON-RPC 进程（codex app-server / grok agent stdio），跑完 fn 就整组杀掉
func quickRPC(name string, cmd *exec.Cmd, cwd string, version bool, fn func(*rpcConn) error) error {
	cmd.Dir = cwd
	cmd.Env = childEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s 失败: %w", name, err)
	}
	done := make(chan struct{})
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		_ = cmd.Wait()
	}()
	var wmu sync.Mutex
	r := newRPC(name, func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		_, err = stdin.Write(append(b, '\n'))
		return err
	}, done, version)
	go func() {
		defer close(done)
		_ = eachLine(stdout, 16<<20, func(line []byte) bool {
			// 这种一问一答用不着它反问什么（登录、审批），来了就回绝
			if m := r.dispatch(line); m != nil && m.isRequest() {
				go r.replyErr(m.ID, -32601, "rcweb 不支持 "+m.Method)
			}
			return true
		})
	}()
	return fn(r)
}
