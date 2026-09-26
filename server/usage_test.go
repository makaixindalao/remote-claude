package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ~/.claude.json 的 utilization 里混着列表和别的对象，不能因为它们整个解析失败
func TestClaudeJSONLimits(t *testing.T) {
	home := t.TempDir()
	doc := `{"cachedUsageUtilization":{"fetchedAtMs":1790216424573,"utilization":{
		"five_hour":{"utilization":8,"resets_at":"2026-09-24T04:29:59.538739+00:00"},
		"seven_day":{"utilization":0,"resets_at":"2026-10-01T01:59:59.538765+00:00"},
		"seven_day_opus":null,
		"extra_usage":{"is_enabled":false,"utilization":null},
		"limits":[{"kind":"session","percent":8}]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &Config{ClaudeDir: filepath.Join(home, ".claude")}}
	got := s.claudeJSONLimits()
	if got.FiveHour == nil || got.FiveHour.Pct != 8 || got.FiveHour.ResetsAt != 1790224199538 {
		t.Fatalf("five_hour = %+v", got.FiveHour)
	}
	if got.SevenDay == nil || got.SevenDay.Pct != 0 {
		t.Fatalf("seven_day = %+v", got.SevenDay)
	}
	if got.UpdatedAt != 1790216424573 {
		t.Fatalf("updatedAt = %d", got.UpdatedAt)
	}
}

func TestNoteRateLimit(t *testing.T) {
	noteRateLimit([]byte(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.15,"resetsAt":1790224200}}}}`))
	liveLimits.mu.Lock()
	defer liveLimits.mu.Unlock()
	if w := liveLimits.v.FiveHour; w == nil || w.Pct != 15 || w.ResetsAt != 1790224200000 {
		t.Fatalf("five_hour = %+v", w)
	}
}
