package main

import (
	"encoding/json"
	"strings"
)

// PermissionAlways 是「总是允许」会写下什么：页面上按钮写成「总是允许 Bash(rcweb deploy:*)」，
// 悬停再说记在哪。只有 claude 的 permission_suggestions 里有这些信息
type PermissionAlways struct {
	Label string `json:"label"`
	Where string `json:"where,omitempty"`
}

// describeSuggestions 把 claude 的 permission_suggestions（PermissionUpdate 数组）翻成一句话。
// 认得的：addRules / replaceRules（behavior=allow）、addDirectories、setMode；别的忽略，一样都不认得就返回 nil
func describeSuggestions(raw json.RawMessage) *PermissionAlways {
	var updates []struct {
		Type     string `json:"type"`
		Behavior string `json:"behavior"`
		Rules    []struct {
			ToolName    string `json:"toolName"`
			RuleContent string `json:"ruleContent"`
		} `json:"rules"`
		Directories []string `json:"directories"`
		Mode        string   `json:"mode"`
		Destination string   `json:"destination"`
	}
	if json.Unmarshal(raw, &updates) != nil {
		return nil
	}
	var parts []string
	where := ""
	for _, u := range updates {
		n := len(parts)
		switch u.Type {
		case "addRules", "replaceRules":
			if u.Behavior != "" && u.Behavior != "allow" {
				continue
			}
			for _, r := range u.Rules {
				if r.RuleContent != "" {
					parts = append(parts, r.ToolName+"("+r.RuleContent+")")
				} else if r.ToolName != "" {
					parts = append(parts, r.ToolName)
				}
			}
		case "addDirectories":
			if len(u.Directories) > 0 {
				parts = append(parts, "目录 "+strings.Join(u.Directories, "、"))
			}
		case "setMode":
			if u.Mode != "" {
				parts = append(parts, "切到 "+u.Mode)
			}
		}
		if where == "" && len(parts) > n {
			where = destinationText[u.Destination]
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return &PermissionAlways{Label: strings.Join(parts, ", "), Where: where}
}

var destinationText = map[string]string{
	"localSettings":   "项目的 .claude/settings.local.json",
	"projectSettings": "项目的 .claude/settings.json",
	"userSettings":    "~/.claude/settings.json",
	"session":         "这次对话里，不写文件",
}
