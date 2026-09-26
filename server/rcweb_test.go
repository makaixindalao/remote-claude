package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseDotEnv(t *testing.T) {
	got := parseDotEnv("# 注释\nRCWEB_PASSWORD=abc\nexport RCWEB_LISTEN=\"127.0.0.1:1\"\nX='a b'\n\nBAD LINE\n")
	want := map[string]string{"RCWEB_PASSWORD": "abc", "RCWEB_LISTEN": "127.0.0.1:1", "X": "a b"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestAuthToken(t *testing.T) {
	a := newAuth("pw", "")
	exp := time.Now().Add(time.Hour).Unix()
	tok := a.token(exp)
	if !a.validToken(tok) {
		t.Fatal("fresh token rejected")
	}
	if a.validToken(a.token(time.Now().Add(-time.Minute).Unix())) {
		t.Error("expired token accepted")
	}
	// 改过期时间、换密码都要失效
	if a.validToken(strconv.FormatInt(exp+3600, 10) + tok[strings.Index(tok, "."):]) {
		t.Error("tampered expiry accepted")
	}
	if newAuth("other", "").validToken(tok) {
		t.Error("token survived password change")
	}
}

func TestLoginLockoutPerIP(t *testing.T) {
	a := newAuth("pw", "")
	for i := 0; i < maxFailures; i++ {
		a.failures["1.2.3.4"] = append(a.failures["1.2.3.4"], time.Now())
	}
	a.failures["5.6.7.8"] = []time.Time{time.Now().Add(-2 * failureWindow)}
	if locked, _ := a.locked("1.2.3.4"); !locked {
		t.Error("IP over the limit not locked")
	}
	// 别人试错不能把你锁在外面
	if locked, _ := a.locked("9.9.9.9"); locked {
		t.Error("unrelated IP locked")
	}
	if _, ok := a.failures["5.6.7.8"]; ok {
		t.Error("expired failures not pruned")
	}

	for _, c := range []struct{ remote, want string }{
		{"1.2.3.4:5555", "1.2.3.4"},
		{"[2001:db8:1:2:aaaa::1]:5555", "2001:db8:1:2::"},
		{"[2001:db8:1:2:bbbb::9]:5555", "2001:db8:1:2::"},
	} {
		r := httptest.NewRequest("POST", "/api/login", nil)
		r.RemoteAddr = c.remote
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%s) = %s, want %s", c.remote, got, c.want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		host, origin, fwd string
		ok                bool
	}{
		{"localhost:7681", "http://localhost:7681", "", true},
		{"localhost:7681", "https://evil.example", "", false},
		{"localhost:7681", "", "", false},
		{"127.0.0.1:7681", "https://vps.tail.ts.net", "vps.tail.ts.net", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "http://"+c.host+"/api/x", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.fwd != "" {
			r.Header.Set("X-Forwarded-Host", c.fwd)
		}
		if got := sameOrigin(r); got != c.ok {
			t.Errorf("host=%s origin=%s fwd=%s: got %v", c.host, c.origin, c.fwd, got)
		}
	}
}

func TestMangle(t *testing.T) {
	// 和 bin/rcsync 里实测的对照一致
	if got := mangle("/root/my_sdk"); got != "-root-my-sdk" {
		t.Error(got)
	}
	if got := mangle("/root/mytool/.claude/worktrees/x"); got != "-root-mytool--claude-worktrees-x" {
		t.Error(got)
	}
}

func rec(s string) *record {
	var r record
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		panic(err)
	}
	return &r
}

func TestEntryFromRecord(t *testing.T) {
	if e := entryFromRecord(rec(`{"type":"user","isMeta":true,"message":{"role":"user","content":"skill body"}}`)); e != nil {
		t.Error("meta record should be hidden")
	}
	// 实时聊天（stream-json）里技能正文不带 isMeta，只有 isSynthetic
	if e := entryFromRecord(rec(`{"type":"user","isSynthetic":true,"parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /x"}]}}`)); e != nil {
		t.Error("synthetic record should be hidden")
	}
	if e := entryFromRecord(rec(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"role":"assistant","content":[{"type":"text","text":"sub"}]}}`)); e != nil {
		t.Error("subagent record should be hidden")
	}
	e := entryFromRecord(rec(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"x"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`))
	if e == nil || len(e.Blocks) != 1 || e.Blocks[0].T != "tool_use" || e.Blocks[0].Name != "Bash" {
		t.Fatalf("empty thinking should be dropped, tool_use kept: %+v", e)
	}
	e = entryFromRecord(rec(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true}]}}`))
	if e == nil || e.Blocks[0].Text != "a\nb" || !e.Blocks[0].IsError || e.Blocks[0].ID != "t1" {
		t.Fatalf("tool_result: %+v", e)
	}
	if e := entryFromRecord(rec(`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"summary"}}`)); e == nil || e.Role != "note" {
		t.Error("compact summary should become a note")
	}
}

func TestPromptTitle(t *testing.T) {
	if got := promptTitle(rec(`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`)); got != "" {
		t.Errorf("command wrapper used as title: %q", got)
	}
	if got := promptTitle(rec(`{"type":"user","message":{"role":"user","content":"  帮我\n  看看  这个 "}}`)); got != "帮我 看看 这个" {
		t.Errorf("got %q", got)
	}
}

func TestBuildDecision(t *testing.T) {
	req := &PermissionReq{Tool: "Bash", rawInput: json.RawMessage(`{"command":"ls"}`), CanAlways: true, suggestions: json.RawMessage(`[{"type":"addRules"}]`)}
	d := buildDecision(req, PermissionAnswer{Choice: "always"})
	if d["behavior"] != "allow" || d["updatedPermissions"] == nil {
		t.Errorf("always: %v", d)
	}
	if d := buildDecision(req, PermissionAnswer{Choice: "deny"}); d["behavior"] != "deny" || d["interrupt"] != false {
		t.Errorf("deny: %v", d)
	}
	ask := &PermissionReq{Tool: "AskUserQuestion", rawInput: json.RawMessage(`{"questions":[{"question":"Q?"}]}`)}
	d = buildDecision(ask, PermissionAnswer{Choice: "allow", Answers: map[string]string{"Q?": "A"}})
	var in map[string]any
	_ = json.Unmarshal(d["updatedInput"].(json.RawMessage), &in)
	if in["answers"].(map[string]any)["Q?"] != "A" || in["questions"] == nil {
		t.Errorf("AskUserQuestion answers not merged: %v", in)
	}
}

func TestExpandPasted(t *testing.T) {
	h := historyLine{
		Display:        "看这个 [Pasted text #1 +2 lines] 和 [Pasted text #2]",
		PastedContents: map[string]json.RawMessage{"1": json.RawMessage(`{"id":1,"type":"text","content":"x\ny"}`)},
	}
	if got := expandPasted(h); got != "看这个 x\ny 和 [Pasted text #2]" {
		t.Errorf("got %q", got)
	}
}

func TestValidTmuxName(t *testing.T) {
	for name, ok := range map[string]bool{"cc-myapp": true, "cc-中文": true, "a.b": false, "a:b": false, "-x": false, "": false, "a\nb": false} {
		if validTmuxName(name) != ok {
			t.Errorf("%q: want %v", name, ok)
		}
	}
}

func TestClipKeepsUTF8(t *testing.T) {
	s := strings.Repeat("中", 10)
	got := clip(s, 7) // 7 字节落在第三个字中间
	if !strings.HasPrefix(got, "中中\n") {
		t.Errorf("got %q", got)
	}
}

func TestUnderDirFollowsSymlinks(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspace")
	must(t, os.MkdirAll(filepath.Join(root, "app"), 0o755))
	must(t, os.MkdirAll(filepath.Join(tmp, "other"), 0o755))
	link := filepath.Join(tmp, "mac-workspace") // 模拟 /Users/…/workspace -> /workspace
	must(t, os.Symlink(root, link))
	for p, ok := range map[string]bool{
		root:                           true,
		filepath.Join(root, "app"):     true,
		filepath.Join(link, "app"):     true,
		link:                           true,
		filepath.Join(tmp, "other"):    false,
		filepath.Join(root, "..", "x"): false,
		filepath.Join(root, "missing"): true, // 不存在的交给 isDir 报错
	} {
		if underDir(root, p) != ok {
			t.Errorf("%s: want %v", p, ok)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
