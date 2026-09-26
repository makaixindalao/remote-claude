package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandText(t *testing.T) {
	cases := map[string]string{
		`["/bin/zsh","-lc","printf 'hi' > a.txt"]`:    `printf 'hi' > a.txt`,
		`"/bin/zsh -lc \"ls -la && echo \\\"x\\\"\""`: `ls -la && echo "x"`,
		`"bash -c 'it'\\''s'"`:                        `it's`,
		`"git status"`:                                `git status`,
	}
	for in, want := range cases {
		if got := commandText(json.RawMessage(in)); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestParseCodexItemBothFormats(t *testing.T) {
	// rollout 里的核心格式
	core, ok := parseCodexItem(json.RawMessage(`{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","false"],"aggregated_output":"boom\n","exit_code":1,"status":"completed"}`))
	if !ok || core.Type != "commandExecution" || core.Command != "false" {
		t.Fatalf("core: %+v", core)
	}
	if r := core.toolResult(); !r.IsError || !strings.Contains(r.Text, "退出码 1") {
		t.Errorf("non-zero exit should be an error result: %+v", r)
	}
	// app-server 推的 v2 格式
	v2, ok := parseCodexItem(json.RawMessage(`{"type":"commandExecution","id":"e2","command":"/bin/zsh -lc \"echo hi\"","aggregatedOutput":"hi\n","exitCode":0,"status":"completed"}`))
	if !ok || v2.Command != "echo hi" || v2.toolResult().IsError || v2.toolUse().Name != "Bash" {
		t.Fatalf("v2: %+v", v2)
	}
	msg, _ := parseCodexItem(json.RawMessage(`{"type":"AgentMessage","id":"m","content":[{"type":"Text","text":"好"}],"phase":"final_answer"}`))
	if es := msg.entries(""); len(es) != 1 || es[0].Role != "assistant" || es[0].Blocks[0].Text != "好" {
		t.Errorf("agent message: %+v", es)
	}
	empty, _ := parseCodexItem(json.RawMessage(`{"type":"Reasoning","id":"r","summary_text":[],"raw_content":[]}`))
	if es := empty.entries(""); es != nil {
		t.Errorf("empty reasoning should not show: %+v", es)
	}
}

func TestPatchChanges(t *testing.T) {
	core := patchChanges(json.RawMessage(`{"/b.go":{"type":"update","unified_diff":"@@ -1 +1 @@\n-a\n+b\n"},"/a.md":{"type":"add","content":"x\n"}}`))
	if len(core) != 2 || core[0].Path != "/a.md" || core[0].Kind != "add" || core[0].Diff != "x\n" || core[1].Kind != "update" {
		t.Errorf("core: %+v", core)
	}
	v2 := patchChanges(json.RawMessage(`[{"path":"/c.go","kind":{"type":"delete"},"diff":"y"}]`))
	if len(v2) != 1 || v2[0].Kind != "delete" || v2[0].Path != "/c.go" {
		t.Errorf("v2: %+v", v2)
	}
}

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-09-23T10-00-00-01a0d15e-1d8a-76f3-b47e-1afd63630335.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadCodexEntries(t *testing.T) {
	p := writeLines(t,
		`{"type":"session_meta","payload":{"id":"01a0d15e-1d8a-76f3-b47e-1afd63630335","cwd":"/root/app","source":"cli"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions"}]}}`,
		`{"timestamp":"2026-09-23T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u","content":[{"type":"text","text":"跑一下测试"}]}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["/bin/zsh","-lc","go test"],"aggregated_output":"ok\n","exit_code":0}}}`,
		`{"type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}`,
	)
	es, _, err := readCodexEntries(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range es {
		got = append(got, e.Role+":"+e.Blocks[0].T)
	}
	want := "user:text assistant:tool_use user:tool_result note:text"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}

	f := &codexFile{path: p}
	readCodexMeta(f)
	if f.skip || f.cwd != "/root/app" || f.id != "01a0d15e-1d8a-76f3-b47e-1afd63630335" {
		t.Errorf("meta: %+v", f)
	}
	if got := codexFirstPrompt(p); got != "跑一下测试" {
		t.Errorf("first prompt: %q", got)
	}
}

func TestReadCodexEntriesLegacy(t *testing.T) {
	// 没有 item_completed 的老 rollout：退回去拼 response_item，系统注入的 user 消息不显示
	p := writeLines(t,
		`{"type":"session_meta","payload":{"id":"x","cwd":"/root/app","source":{"subagent":{"thread_spawn":{}}}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>…</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"ls\"]}","call_id":"k"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"k","output":"{\"output\":\"a.txt\"}"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	)
	es, _, _ := readCodexEntries(p, 0)
	if len(es) != 4 || es[0].Blocks[0].Text != "hello" || es[1].Blocks[0].Name != "Bash" || es[2].Blocks[0].Text != "a.txt" || es[3].Blocks[0].Text != "done" {
		t.Errorf("legacy: %+v", es)
	}
	f := &codexFile{path: p}
	readCodexMeta(f)
	if !f.skip {
		t.Error("subagent thread should be skipped")
	}
}

func TestACPBuilder(t *testing.T) {
	var out []Entry
	var deltas []string
	b := newACPBuilder(func(e Entry) { out = append(out, e) }, func(s string) { deltas = append(deltas, s) })
	for _, u := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"想"}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"一想"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"我来"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"改"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t1","title":"search_replace","rawInput":{"file_path":"/a","old_string":"x","new_string":"y"},"_meta":{"x.ai/tool":{"name":"search_replace"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed","content":[{"type":"diff","path":"/a","oldText":"x","newText":"y"}]}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"t1","status":"completed"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"ws_1","title":"Web search:","status":"completed","rawOutput":{"action":{"query":"acp","sources":[{"url":"https://x"}]}}}`,
	} {
		b.update(json.RawMessage(u), "")
	}
	b.flush()
	var got []string
	for _, e := range out {
		got = append(got, e.Role+":"+e.Blocks[0].T+":"+e.Blocks[0].Name)
	}
	want := "assistant:thinking: assistant:text: assistant:tool_use:Edit user:tool_result: assistant:tool_use:WebSearch user:tool_result:"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v\nwant %s", got, want)
	}
	if out[0].Blocks[0].Text != "想一想" || out[1].Blocks[0].Text != "我来改" || strings.Join(deltas, "") != "我来改" {
		t.Errorf("chunks not merged: %+v / %v", out[:2], deltas)
	}
	if out[3].Blocks[0].Text != "已修改 /a" || out[5].Blocks[0].Text != "搜索：acp\nhttps://x" {
		t.Errorf("results: %q / %q", out[3].Blocks[0].Text, out[5].Blocks[0].Text)
	}
}

func TestGrokTool(t *testing.T) {
	name, in := grokTool("read_file", json.RawMessage(`{"variant":"ReadFile","target_file":"/a","limit":5}`))
	var m map[string]any
	_ = json.Unmarshal(in, &m)
	if name != "Read" || m["file_path"] != "/a" || m["variant"] != nil || m["limit"] != float64(5) {
		t.Errorf("read_file → %s %v", name, m)
	}
	if name, _ := grokTool("mystery_tool", json.RawMessage(`{"a":1}`)); name != "mystery_tool" {
		t.Errorf("unknown tool renamed: %s", name)
	}
}

func TestCodexDecisions(t *testing.T) {
	allow, always, deny := codexDecisions([]json.RawMessage{
		json.RawMessage(`"accept"`), json.RawMessage(`{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["ls"]}}`), json.RawMessage(`"cancel"`),
	})
	if allow != "accept" || deny != "cancel" || always == nil {
		t.Errorf("got %v %v %v", allow, always, deny)
	}
	allow, always, deny = codexDecisions(nil) // 老版本不带 availableDecisions
	if allow != "accept" || always != "acceptForSession" || deny != "decline" {
		t.Errorf("defaults: %v %v %v", allow, always, deny)
	}
}

func TestOwnedBy(t *testing.T) {
	paths := []string{"/root/app", "/root/app/server", "/root/apple"}
	for cwd, want := range map[string]string{
		"/root/app":            "/root/app",
		"/root/app/ui":         "/root/app",
		"/root/app/server/cmd": "/root/app/server",
		"/root/apple":          "/root/apple",
		"/root/other":          "",
	} {
		if got := ownedBy(cwd, paths); got != want {
			t.Errorf("%s: got %q, want %q", cwd, got, want)
		}
	}
}

func TestValidateStart(t *testing.T) {
	o := StartOpts{Agent: "codex"}
	if err := validateStart(&o); err != nil || o.PermissionMode != "auto" {
		t.Errorf("codex default mode: %v %q", err, o.PermissionMode)
	}
	if err := validateStart(&StartOpts{Agent: "codex", PermissionMode: "bypassPermissions"}); err == nil {
		t.Error("claude mode accepted for codex")
	}
	if err := validateStart(&StartOpts{Agent: "grok", Effort: "xhigh"}); err != nil {
		t.Error(err)
	}
	if err := validateStart(&StartOpts{Agent: "gemini"}); err == nil {
		t.Error("unknown agent accepted")
	}
}
