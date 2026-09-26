package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// 新对话用什么模型 / effort / 权限模式。设置页里按 CLI 各设各的，每一项都可以选「和上次一致」；
// 上次用的也记在这里（输入框里一改就记），所以手机和电脑是同一套。存在 settings.json 的 chat 下。

// ChatPrefs：Defaults 里空着的一项 = 和上次一致（用 Last 里的）
type ChatPrefs struct {
	Defaults ChatChoice  `json:"defaults"`
	Last     *ChatChoice `json:"last,omitempty"`
}

// ChatChoice 的 Effort：空 = 不指定，用模型自己的那一档；Defaults 里空 = 和上次一致，effortByModel 才是不指定
type ChatChoice struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

const effortByModel = "model"

func (c ChatChoice) validate(agent string, defaults bool) error {
	if !modelRe.MatchString(c.Model) {
		return fmt.Errorf("模型名不合法")
	}
	if c.Effort != "" && !validEffort(agent, c.Effort) && (!defaults || c.Effort != effortByModel) {
		return fmt.Errorf("不认识的 effort %q", c.Effort)
	}
	if c.Mode != "" && !validMode(agent, c.Mode) {
		return fmt.Errorf("不认识的权限模式 %q", c.Mode)
	}
	return nil
}

func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	out := map[string]ChatPrefs{}
	for a, p := range s.settings.get().Chat {
		if p != nil {
			out[a] = *p
		}
	}
	writeJSON(w, out)
}

// handlePrefsSave：POST /api/prefs {agent, defaults} 存设置页里的默认值；
// POST /api/prefs {agent, last} 记下输入框里刚选的（两样可以一起带）
func (s *Server) handlePrefsSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent    string      `json:"agent"`
		Defaults *ChatChoice `json:"defaults"`
		Last     *ChatChoice `json:"last"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	agent := normAgent(body.Agent)
	if !validAgent(agent) {
		writeErr(w, http.StatusBadRequest, "不认识的 agent")
		return
	}
	for _, c := range []struct {
		v        *ChatChoice
		defaults bool
	}{{body.Defaults, true}, {body.Last, false}} {
		if c.v == nil {
			continue
		}
		if err := c.v.validate(agent, c.defaults); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var saved ChatPrefs
	err := s.settings.update(func(v *Settings) {
		next := map[string]*ChatPrefs{}
		for a, p := range v.Chat {
			next[a] = p
		}
		p := ChatPrefs{}
		if old := next[agent]; old != nil {
			p = *old
		}
		if body.Defaults != nil {
			p.Defaults = *body.Defaults
		}
		if body.Last != nil {
			last := *body.Last
			p.Last = &last
		}
		next[agent] = &p
		v.Chat = next
		saved = p
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, saved)
}
