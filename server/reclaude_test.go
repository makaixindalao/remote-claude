package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 下面几段输出是照 reclaude v1.4.0 实际打印的抄的
func TestParseOrgs(t *testing.T) {
	out := "Available organizations:\n" +
		"* 4216\tme@example.com\tpersonal\tme@example.com\n" +
		"  1694\tSystem Carpool 20260507-030824\tteam\tboss@example.com\n" +
		"Switch organization: reclaude org use <org_id>\n"
	got := parseOrgs(out)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if o := got[0]; o.ID != "4216" || !o.Current || o.Type != "personal" || o.Email != "me@example.com" {
		t.Fatalf("org0 = %+v", o)
	}
	if o := got[1]; o.ID != "1694" || o.Current || o.Name != "System Carpool 20260507-030824" || o.Type != "team" {
		t.Fatalf("org1 = %+v", o)
	}
}

func TestParseKV(t *testing.T) {
	out := "url:    https://la.route.reclaude.ai\nsource: device.json\nmode:   manual\n" +
		"note:   running daemon may still use old URL; run reclaude status to inspect live state\n"
	got := parseKV(out)
	if len(got) != 4 || got[0] != (kv{"url", "https://la.route.reclaude.ai"}) || got[2] != (kv{"mode", "manual"}) {
		t.Fatalf("got %+v", got)
	}
	status := parseKV("daemon_running: true\ndaemon_started_at: 2026-09-22 07:01:10\nintercept_domains: v0:0 (0 dynamic)\n")
	if len(status) != 3 || status[1].Value != "2026-09-22 07:01:10" || status[2].Value != "v0:0 (0 dynamic)" {
		t.Fatalf("status = %+v", status)
	}
}

func TestParseGatewayTest(t *testing.T) {
	out := "OK      3. https://la.route.reclaude.ai           765ms\n" +
		"FAIL    2. https://asia.route.reclaude.ai         dial tcp: i/o timeout\n" +
		"Select gateway number (Enter=fastest, q=cancel): \n"
	got := parseGatewayTest(out)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if g := got[0]; !g.OK || g.URL != "https://la.route.reclaude.ai" || g.Detail != "765ms" {
		t.Fatalf("g0 = %+v", g)
	}
	if g := got[1]; g.OK || g.Status != "FAIL" || g.Detail != "dial tcp: i/o timeout" {
		t.Fatalf("g1 = %+v", g)
	}
	// 只测一个地址是另一种写法
	if g := parseGatewayTest("ok: https://la.route.reclaude.ai (1.747s)"); len(g) != 1 || !g[0].OK || g[0].Detail != "1.747s" {
		t.Fatalf("single ok = %+v", g)
	}
	g := parseGatewayTest(`fail: https://nonexistent.invalid (Post "https://nonexistent.invalid/proxy": EOF)`)
	if len(g) != 1 || g[0].OK || g[0].Status != "FAIL" || g[0].Detail != `Post "https://nonexistent.invalid/proxy": EOF` {
		t.Fatalf("single fail = %+v", g)
	}
}

func TestValidGatewayURL(t *testing.T) {
	for _, u := range []string{"https://la.route.reclaude.ai", "http://127.0.0.1:8080/x"} {
		if !validGatewayURL(u) {
			t.Errorf("%q 应该合法", u)
		}
	}
	for _, u := range []string{"", "--help", "la.route.reclaude.ai", "ftp://x", "https://", "https://a b"} {
		if validGatewayURL(u) {
			t.Errorf("%q 不该合法", u)
		}
	}
	for _, id := range []string{"--help", "-1", "", "a b"} {
		if orgIDRe.MatchString(id) {
			t.Errorf("组织 ID %q 不该合法", id)
		}
	}
}

// 开关写进 settings.json，重启后还在；打开后起 claude 的地方都换成 reclaude
func TestReclaudeSwitch(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "reclaude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &Config{ClaudeBin: "claude", ReclaudeBin: fake}, settings: newSettingsStore(dir)}
	if got := s.agentBin(agentClaude); got != "claude" {
		t.Fatalf("默认应该是 claude，得到 %q", got)
	}
	if err := s.settings.update(func(v *Settings) { v.UseReclaude = true }); err != nil {
		t.Fatal(err)
	}
	if got := s.agentBin(agentClaude); got != fake {
		t.Fatalf("打开后应该是 %q，得到 %q", fake, got)
	}
	if !newSettingsStore(dir).get().UseReclaude {
		t.Fatal("重新读 settings.json 后开关丢了")
	}
}
