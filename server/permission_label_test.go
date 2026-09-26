package main

import (
	"encoding/json"
	"testing"
)

func TestDescribeSuggestions(t *testing.T) {
	cases := []struct {
		in    string
		label string
		where string
	}{
		{`[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"rcweb deploy:*"}],"behavior":"allow","destination":"localSettings"}]`,
			"Bash(rcweb deploy:*)", "项目的 .claude/settings.local.json"},
		{`[{"type":"addRules","rules":[{"toolName":"WebFetch"}],"behavior":"allow","destination":"session"}]`,
			"WebFetch", "这次对话里，不写文件"},
		{`[{"type":"addDirectories","directories":["/tmp","/var/log"],"destination":"session"},{"type":"setMode","mode":"acceptEdits","destination":"session"}]`,
			"目录 /tmp、/var/log, 切到 acceptEdits", "这次对话里，不写文件"},
	}
	for _, c := range cases {
		got := describeSuggestions(json.RawMessage(c.in))
		if got == nil || got.Label != c.label || got.Where != c.where {
			t.Errorf("%s: got %+v, want %q / %q", c.in, got, c.label, c.where)
		}
	}
	for _, in := range []string{``, `null`, `[]`, `[{"type":"removeRules","rules":[{"toolName":"Bash"}]}]`, `[{"type":"addRules","behavior":"deny","rules":[{"toolName":"Bash"}]}]`} {
		if got := describeSuggestions(json.RawMessage(in)); got != nil {
			t.Errorf("%q: want nil, got %+v", in, got)
		}
	}
}
