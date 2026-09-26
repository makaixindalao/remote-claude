package main

import (
	"encoding/json"
	"testing"
)

func TestWithDefaultEfforts(t *testing.T) {
	raw := json.RawMessage(`{"commands":[{"name":"x"}],"models":[{"value":"default"},{"value":"sonnet"},{"value":"haiku"},{"value":"broken"}]}`)
	got := withDefaultEfforts(raw, func(m string) (string, bool) {
		switch m {
		case "default":
			return "medium", true
		case "sonnet":
			return "high", true
		case "haiku":
			return "", true // 不支持 effort
		}
		return "", false
	})
	var cat struct {
		Commands []map[string]string `json:"commands"`
		Models   []map[string]any    `json:"models"`
	}
	if err := json.Unmarshal(got, &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Commands) != 1 {
		t.Fatalf("别的字段不该动: %s", got)
	}
	want := map[string]any{"default": "medium", "sonnet": "high", "haiku": ""}
	for _, m := range cat.Models {
		e, ok := m["defaultEffort"]
		if w, has := want[m["value"].(string)]; has != ok || e != w {
			t.Errorf("%v: defaultEffort=%v(%v) want %v", m["value"], e, ok, w)
		}
	}

	// 第一个就问不出来（老版本 CLI）：原样返回，后面的也不问
	asked := 0
	got = withDefaultEfforts(raw, func(string) (string, bool) { asked++; return "", false })
	if asked != 1 || string(got) != string(raw) {
		t.Fatalf("asked=%d got=%s", asked, got)
	}
}

func TestCarryDefaultEfforts(t *testing.T) {
	prev := json.RawMessage(`{"models":[{"value":"default","defaultEffort":"medium"},{"value":"haiku","defaultEffort":""}]}`)
	next := json.RawMessage(`{"commands":[],"models":[{"value":"default"},{"value":"haiku"},{"value":"new"}]}`)
	var cat struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(carryDefaultEfforts(prev, next), &cat); err != nil {
		t.Fatal(err)
	}
	if cat.Models[0]["defaultEffort"] != "medium" || cat.Models[1]["defaultEffort"] != "" {
		t.Fatalf("没抄过来: %v", cat.Models)
	}
	if _, ok := cat.Models[2]["defaultEffort"]; ok {
		t.Fatal("prev 里没有的模型不该有")
	}
	if got := carryDefaultEfforts(json.RawMessage(`{}`), next); string(got) != string(next) {
		t.Fatalf("prev 没有模型时原样返回: %s", got)
	}
}

func TestStoreCatalogSkipsWithoutDefaults(t *testing.T) {
	s := &Server{catalogs: catalogCache{items: map[string]catalogItem{}}}
	bare := json.RawMessage(`{"models":[{"value":"default"}]}`)
	s.storeCatalog(agentClaude, "/p", bare)
	if len(s.catalogs.items) != 0 {
		t.Fatal("claude 没有默认 effort 的目录不该进缓存")
	}
	s.storeCatalog(agentCodex, "/p", bare)
	if len(s.catalogs.items) != 1 {
		t.Fatal("codex 的照常缓存")
	}
	s.catalogs.items[agentClaude+"\x00/p"] = catalogItem{raw: json.RawMessage(`{"models":[{"value":"default","defaultEffort":"medium"}]}`)}
	got := s.storeCatalog(agentClaude, "/p", json.RawMessage(`{"commands":[{"name":"new"}],"models":[{"value":"default"}]}`))
	if !hasDefaultEfforts(got) || string(s.catalogs.items[agentClaude+"\x00/p"].raw) != string(got) {
		t.Fatalf("应该抄上默认 effort 并更新缓存: %s", got)
	}
}

func TestParseApplied(t *testing.T) {
	m, e, ok := parseApplied(json.RawMessage(`{"effective":{},"applied":{"model":"claude-opus-5-5[1m]","effort":"medium"}}`))
	if !ok || m != "claude-opus-5-5[1m]" || e != "medium" {
		t.Fatalf("%q %q %v", m, e, ok)
	}
	if _, e, ok = parseApplied(json.RawMessage(`{"applied":{"model":"claude-haiku-4-5","effort":null}}`)); !ok || e != "" {
		t.Fatalf("null effort: %q %v", e, ok)
	}
	if _, _, ok = parseApplied(json.RawMessage(`{"effective":{}}`)); ok {
		t.Fatal("没有 applied 应该 ok=false")
	}
}

func TestEntryModel(t *testing.T) {
	e := entryFromRecord(rec(`{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}]}}`))
	if e == nil || e.Model != "claude-opus-4-8" {
		t.Fatalf("%+v", e)
	}
	if e := entryFromRecord(rec(`{"type":"assistant","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"err"}]}}`)); e == nil || e.Model != "" {
		t.Fatalf("synthetic: %+v", e)
	}
}

func TestChatChoiceValidate(t *testing.T) {
	ok := []struct {
		agent    string
		c        ChatChoice
		defaults bool
	}{
		{agentClaude, ChatChoice{}, true},
		{agentClaude, ChatChoice{Model: "opus[1m]", Effort: "xhigh", Mode: "acceptEdits"}, false},
		{agentClaude, ChatChoice{Effort: effortByModel}, true},
		{agentCodex, ChatChoice{Model: "gpt-6-sol", Effort: "ultra", Mode: "full-access"}, false},
	}
	for _, c := range ok {
		if err := c.c.validate(c.agent, c.defaults); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []struct {
		agent    string
		c        ChatChoice
		defaults bool
	}{
		{agentClaude, ChatChoice{Effort: effortByModel}, false}, // 只有默认值里能写「跟着模型」
		{agentClaude, ChatChoice{Effort: "ultra"}, true},
		{agentClaude, ChatChoice{Mode: "full-access"}, true},
		{agentClaude, ChatChoice{Model: "a b"}, true},
	}
	for _, c := range bad {
		if err := c.c.validate(c.agent, c.defaults); err == nil {
			t.Errorf("%+v 应该不合法", c)
		}
	}
}
