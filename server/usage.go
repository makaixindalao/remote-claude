package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 5 小时 / 7 天额度，照 zhima-statusline 的原则：不为这个发任何网络请求，只读现成的数据。两个来源取较新的：
//
//  1. 网页对话里 claude 发的 rate_limit_event：rate_limit_info.unifiedWindows.{five_hour,seven_day}
//     （utilization 是 0–1 的比例，resetsAt 是秒）。有对话在跑时最新
//  2. Claude Code 自己缓存在 ~/.claude.json 的 cachedUsageUtilization（utilization 是百分比，
//     resets_at 是 ISO 时间，fetchedAtMs 是抓取时间）。没开网页对话时靠它
//
// 窗口过了重置时间就当 0%：额度已经重新开始算了。

type limitWindow struct {
	Pct      float64 `json:"pct"`      // 0–100
	ResetsAt int64   `json:"resetsAt"` // unix 毫秒，0 = 不知道
}

type limits struct {
	FiveHour  *limitWindow `json:"fiveHour,omitempty"`
	SevenDay  *limitWindow `json:"sevenDay,omitempty"`
	UpdatedAt int64        `json:"updatedAt"` // unix 毫秒
}

var liveLimits struct {
	mu sync.Mutex
	v  limits
}

// noteRateLimit：claude driver 收到 rate_limit_event 时调
func noteRateLimit(line []byte) {
	var ev struct {
		Info struct {
			Windows struct {
				FiveHour *struct {
					Utilization float64 `json:"utilization"`
					ResetsAt    int64   `json:"resetsAt"`
				} `json:"five_hour"`
				SevenDay *struct {
					Utilization float64 `json:"utilization"`
					ResetsAt    int64   `json:"resetsAt"`
				} `json:"seven_day"`
			} `json:"unifiedWindows"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	w := ev.Info.Windows
	if w.FiveHour == nil && w.SevenDay == nil {
		return
	}
	liveLimits.mu.Lock()
	defer liveLimits.mu.Unlock()
	if w.FiveHour != nil {
		liveLimits.v.FiveHour = &limitWindow{Pct: w.FiveHour.Utilization * 100, ResetsAt: w.FiveHour.ResetsAt * 1000}
	}
	if w.SevenDay != nil {
		liveLimits.v.SevenDay = &limitWindow{Pct: w.SevenDay.Utilization * 100, ResetsAt: w.SevenDay.ResetsAt * 1000}
	}
	liveLimits.v.UpdatedAt = time.Now().UnixMilli()
}

var cachedLimits struct {
	mu    sync.Mutex
	mtime time.Time
	v     limits
}

// claudeJSONLimits 读 ~/.claude.json 里 Claude Code 自己缓存的额度，按文件修改时间缓存解析结果
func (s *Server) claudeJSONLimits() limits {
	path := filepath.Join(filepath.Dir(s.cfg.ClaudeDir), ".claude.json")
	fi, err := os.Stat(path)
	if err != nil {
		return limits{}
	}
	cachedLimits.mu.Lock()
	defer cachedLimits.mu.Unlock()
	if fi.ModTime().Equal(cachedLimits.mtime) {
		return cachedLimits.v
	}
	// utilization 里还混着 limits 列表、extra_usage 之类别的形状，只按名字取这两个窗口
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	var doc struct {
		Cached *struct {
			FetchedAtMs int64 `json:"fetchedAtMs"`
			Utilization struct {
				FiveHour *window `json:"five_hour"`
				SevenDay *window `json:"seven_day"`
			} `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &doc) != nil || doc.Cached == nil {
		return limits{}
	}
	win := func(u *window) *limitWindow {
		if u == nil || u.Utilization == nil {
			return nil
		}
		w := &limitWindow{Pct: *u.Utilization}
		if t, err := time.Parse(time.RFC3339Nano, u.ResetsAt); err == nil {
			w.ResetsAt = t.UnixMilli()
		}
		return w
	}
	u := doc.Cached.Utilization
	cachedLimits.mtime = fi.ModTime()
	cachedLimits.v = limits{FiveHour: win(u.FiveHour), SevenDay: win(u.SevenDay), UpdatedAt: doc.Cached.FetchedAtMs}
	return cachedLimits.v
}

func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	liveLimits.mu.Lock()
	v := liveLimits.v
	liveLimits.mu.Unlock()
	if c := s.claudeJSONLimits(); c.UpdatedAt > v.UpdatedAt {
		v = c
	}
	// 拷一份再改：v 里的指针指向缓存，别动缓存本身
	now := time.Now().UnixMilli()
	fresh := func(win *limitWindow) *limitWindow {
		if win == nil {
			return nil
		}
		cp := *win
		if cp.ResetsAt > 0 && cp.ResetsAt < now {
			cp.Pct, cp.ResetsAt = 0, 0 // 已经重置过了
		}
		return &cp
	}
	v.FiveHour, v.SevenDay = fresh(v.FiveHour), fresh(v.SevenDay)
	writeJSON(w, v)
}
