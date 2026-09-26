package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLineDiff(t *testing.T) {
	got := lineDiff([]string{"a", "b", "c", "d"}, []string{"a", "x", "c", "d", "e"})
	want := []string{" a", "-b", "+x", " c", " d", "+e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	if got := lineDiff(nil, []string{"n"}); !reflect.DeepEqual(got, []string{"+n"}) {
		t.Errorf("empty old: %q", got)
	}
}

func TestFileChangesFromInput(t *testing.T) {
	edit := fileChanges("Edit", json.RawMessage(`{"file_path":"/p/a.go","old_string":"x := 1\ny := 2","new_string":"x := 1\ny := 3\nz := 4"}`), true)
	if len(edit) != 1 || edit[0].Add != 2 || edit[0].Del != 1 || edit[0].Path != "/p/a.go" {
		t.Fatalf("edit: %+v", edit)
	}
	if h := edit[0].Hunks[0]; h.OldStart != 0 || h.NewStart != 0 || len(h.Lines) != 4 {
		t.Errorf("edit hunk: %+v", h)
	}
	// 卡片用的只有行数
	if s := fileChanges("Edit", json.RawMessage(`{"file_path":"/p/a.go","old_string":"a","new_string":"b"}`), false); s[0].Hunks != nil || s[0].Add != 1 {
		t.Errorf("stats only: %+v", s)
	}
	multi := fileChanges("MultiEdit", json.RawMessage(`{"file_path":"/p/b.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"c\nd"}]}`), true)
	if len(multi) != 1 || multi[0].Add != 2 || multi[0].Del != 1 || len(multi[0].Hunks) != 2 {
		t.Errorf("multi: %+v", multi)
	}
	write := fileChanges("Write", json.RawMessage(`{"file_path":"/p/c.txt","content":"1\n2\n3\n"}`), true)
	if write[0].Add != 3 || write[0].Hunks[0].NewStart != 1 {
		t.Errorf("write: %+v", write)
	}
	patch := fileChanges("Patch", json.RawMessage(`{"changes":[
		{"path":"/p/d.go","kind":"update","diff":"@@ -10,3 +10,3 @@\n a\n-b\n+c\n d\n"},
		{"path":"/p/e.go","kind":"add","diff":"new\nfile\n"}]}`), true)
	if len(patch) != 2 || patch[0].Add != 1 || patch[0].Del != 1 || patch[0].Hunks[0].OldStart != 10 || patch[1].Status != "add" || patch[1].Add != 2 {
		t.Errorf("patch: %+v", patch)
	}
	if fileChanges("Bash", json.RawMessage(`{"command":"ls"}`), true) != nil {
		t.Error("Bash should have no file changes")
	}
}

func TestResultChanges(t *testing.T) {
	upd := resultChanges(json.RawMessage(`{"filePath":"/p/a.go","oldString":"x","newString":"y","originalFile":"…",
		"structuredPatch":[{"oldStart":5,"oldLines":3,"newStart":5,"newLines":4,"lines":[" a","-x","+y","+z"," b"]}]}`))
	if len(upd) != 1 || upd[0].Add != 2 || upd[0].Del != 1 || upd[0].Hunks[0].OldStart != 5 {
		t.Errorf("update: %+v", upd)
	}
	create := resultChanges(json.RawMessage(`{"type":"create","filePath":"/p/n.go","content":"a\nb","structuredPatch":[]}`))
	if len(create) != 1 || create[0].Status != "add" || create[0].Add != 2 {
		t.Errorf("create: %+v", create)
	}
	for _, raw := range []string{`"Error: boom"`, `{"stdout":"x"}`, `{"type":"text","file":{"filePath":"/p/a"}}`, ``} {
		if resultChanges(json.RawMessage(raw)) != nil {
			t.Errorf("%s: want nil", raw)
		}
	}
}

func TestFinishTruncatesButCountsAll(t *testing.T) {
	fc := wholeFile("big.txt", "add", strings.Repeat("x\n", maxHunkLines+10))
	if fc.Add != maxHunkLines+10 || !fc.Truncated || len(fc.Hunks[0].Lines) != maxHunkLines {
		t.Errorf("add=%d truncated=%v lines=%d", fc.Add, fc.Truncated, len(fc.Hunks[0].Lines))
	}
}

func TestEntryFromRecordAttachesResultFiles(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},
		"toolUseResult":{"filePath":"/p/a.go","structuredPatch":[{"oldStart":1,"oldLines":1,"newStart":1,"newLines":1,"lines":["-a","+b"]}]}}`
	var r record
	must(t, json.Unmarshal([]byte(line), &r))
	e := entryFromRecord(&r)
	if e == nil || len(e.Blocks[0].Files) != 1 || e.Blocks[0].Files[0].Add != 1 {
		t.Fatalf("got %+v", e)
	}
	// stream-json 里的字段名
	line = strings.Replace(line, `"toolUseResult"`, `"tool_use_result"`, 1)
	r = record{}
	must(t, json.Unmarshal([]byte(line), &r))
	if e := entryFromRecord(&r); len(e.Blocks[0].Files) != 1 {
		t.Errorf("tool_use_result not picked up: %+v", e)
	}

	use := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/p/a.go","old_string":"a","new_string":"b"}}]}}`
	r = record{}
	must(t, json.Unmarshal([]byte(use), &r))
	if b := entryFromRecord(&r).Blocks[0]; len(b.Files) != 1 || b.Files[0].Hunks != nil || b.Files[0].Del != 1 {
		t.Errorf("tool_use files: %+v", b.Files)
	}
}

func TestLiteKeepsStatsDetailsHaveHunks(t *testing.T) {
	hunk := []Hunk{{OldStart: 1, OldLines: 1, NewStart: 1, NewLines: 1, Lines: []string{"-a", "+b"}}}
	entries := []Entry{
		{Role: "assistant", Blocks: []Block{{T: "tool_use", ID: "t1", Name: "Edit", Input: json.RawMessage(`{"file_path":"/p/a.go","old_string":"a","new_string":"b"}`),
			Files: []FileChange{{Path: "/p/a.go", Add: 1, Del: 1}}}}},
		{Role: "user", Blocks: []Block{{T: "tool_result", ID: "t1", Text: "ok", Files: []FileChange{{Path: "/p/a.go", Add: 1, Del: 1, Hunks: hunk}}}}},
		{Role: "assistant", Blocks: []Block{{T: "tool_use", ID: "t2", Name: "Write", Input: json.RawMessage(`{"file_path":"/p/n.txt","content":"x\ny"}`)}}},
	}
	lite := liteEntries(entries)
	if f := lite[1].Blocks[0].Files; len(f) != 1 || f[0].Hunks != nil || f[0].Add != 1 {
		t.Errorf("lite result files: %+v", f)
	}
	if entries[1].Blocks[0].Files[0].Hunks == nil {
		t.Error("liteEntries modified the cached entries")
	}
	d := findTools(entries, map[string]bool{"t1": true, "t2": true})
	if len(d["t1"].Files) != 1 || d["t1"].Files[0].Hunks[0].OldStart != 1 {
		t.Errorf("t1 should use the result's patch: %+v", d["t1"].Files)
	}
	if len(d["t2"].Files) != 1 || d["t2"].Files[0].Add != 2 || len(d["t2"].Files[0].Hunks) != 1 {
		t.Errorf("t2 should be computed from input: %+v", d["t2"].Files)
	}
}

func TestParseGitOutputs(t *testing.T) {
	files := parseNameStatus("M\x00a.go\x00R087\x00old.go\x00new.go\x00A\x00n.txt\x00D\x00gone.md\x00")
	want := []FileChange{{Path: "a.go"}, {Path: "new.go", OldPath: "old.go", Status: "rename"}, {Path: "n.txt", Status: "add"}, {Path: "gone.md", Status: "delete"}}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("name-status: %+v", files)
	}
	stats := parseNumstat("3\t1\ta.go\x001\t2\t\x00old.go\x00new.go\x00-\t-\timg.png\x00")
	if stats["a.go"].Add != 3 || stats["new.go"].Del != 2 || !stats["img.png"].Binary {
		t.Errorf("numstat: %+v", stats)
	}
}

func TestDiffHandlers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	must(t, os.MkdirAll(repo, 0o755))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, "line")
	}
	must(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	run("init", "-q", "-b", "main")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	lines[19] = "changed"
	must(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(repo, "new.txt"), []byte("a\nb\n"), 0o644))

	s := newServer(&Config{Root: root, Password: "x"})
	rec := httptest.NewRecorder()
	s.handleDiff(rec, httptest.NewRequest("GET", "/api/diff?cwd="+repo, nil))
	var sum diffSummary
	must(t, json.Unmarshal(rec.Body.Bytes(), &sum))
	if sum.NotRepo != "" || sum.Branch != "main" || len(sum.Files) != 2 {
		t.Fatalf("summary: %s", rec.Body)
	}
	if f := sum.Files[0]; f.Path != "a.txt" || f.Add != 1 || f.Del != 1 {
		t.Errorf("a.txt: %+v", f)
	}
	if f := sum.Files[1]; f.Path != "new.txt" || f.Status != "untracked" || f.Add != 2 {
		t.Errorf("new.txt: %+v", f)
	}

	rec = httptest.NewRecorder()
	s.handleDiffFile(rec, httptest.NewRequest("GET", "/api/diff/file?cwd="+repo+"&path=a.txt", nil))
	var fc FileChange
	must(t, json.Unmarshal(rec.Body.Bytes(), &fc))
	// 上下文是整个文件：40 行没改的 + 删一行加一行
	if len(fc.Hunks) != 1 || len(fc.Hunks[0].Lines) != 41 || fc.Add != 1 || fc.Del != 1 || fc.Hunks[0].OldStart != 1 {
		t.Errorf("file diff: %s", rec.Body)
	}

	rec = httptest.NewRecorder()
	s.handleDiffFile(rec, httptest.NewRequest("GET", "/api/diff/file?cwd="+repo+"&path=new.txt&status=untracked", nil))
	fc = FileChange{}
	must(t, json.Unmarshal(rec.Body.Bytes(), &fc))
	if fc.Add != 2 || fc.Hunks[0].Lines[0] != "+a" {
		t.Errorf("untracked: %s", rec.Body)
	}

	for _, bad := range []string{"../x", "/etc/passwd"} {
		rec = httptest.NewRecorder()
		s.handleDiffFile(rec, httptest.NewRequest("GET", "/api/diff/file?cwd="+repo+"&path="+bad, nil))
		if rec.Code != 400 {
			t.Errorf("%s: code %d", bad, rec.Code)
		}
	}

	// RCWEB_ROOT 外面的目录不看
	rec = httptest.NewRecorder()
	outside := newServer(&Config{Root: filepath.Join(root, "elsewhere"), Password: "x"})
	must(t, os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755))
	outside.handleDiff(rec, httptest.NewRequest("GET", "/api/diff?cwd="+repo, nil))
	sum = diffSummary{}
	must(t, json.Unmarshal(rec.Body.Bytes(), &sum))
	if sum.NotRepo == "" {
		t.Errorf("outside root should be refused: %s", rec.Body)
	}
}

func TestAnchorHunkAddsContext(t *testing.T) {
	file := []string{"1", "2", "3", "4", "new5", "6", "7", "8", "9"}
	h := anchorHunk(newHunk(0, 0, []string{"-old5", "+new5"}), file)
	want := []string{" 2", " 3", " 4", "-old5", "+new5", " 6", " 7", " 8"}
	if h.OldStart != 2 || h.NewStart != 2 || !reflect.DeepEqual(h.Lines, want) {
		t.Errorf("got %+v", h)
	}
	// 贴着文件头：前面不够 3 行就有几行给几行
	if h := anchorHunk(newHunk(0, 0, []string{"-x", "+1"}), file); h.NewStart != 1 || h.Lines[0] != "-x" || len(h.Lines) != 5 {
		t.Errorf("at top: %+v", h)
	}
	// 文件里找不到改完的样子（之后又改过）：原样
	orig := newHunk(0, 0, []string{"-a", "+zzz"})
	if h := anchorHunk(orig, file); !reflect.DeepEqual(h, orig) {
		t.Errorf("not found should be unchanged: %+v", h)
	}
}

func TestAnchorToolsReadsUnderRootOnly(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	must(t, os.WriteFile(p, []byte("package a\n\nfunc A() {\n\treturn 2\n}\n"), 0o644))
	s := newServer(&Config{Root: root, Password: "x"})
	shared := []FileChange{{Path: "a.go", Add: 1, Del: 1, Hunks: []Hunk{newHunk(0, 0, []string{"-\treturn 1", "+\treturn 2"})}}}
	tools := map[string]*toolDetail{"t1": {Files: shared}}
	s.anchorTools(tools, root)
	h := tools["t1"].Files[0].Hunks[0]
	if h.NewStart != 1 || len(h.Lines) != 6 || h.Lines[0] != " package a" {
		t.Errorf("anchored: %+v", h)
	}
	if shared[0].Hunks[0].NewStart != 0 {
		t.Error("anchorTools modified the shared slice")
	}
	outside := newServer(&Config{Root: filepath.Join(root, "sub"), Password: "x"})
	tools = map[string]*toolDetail{"t1": {Files: shared}}
	outside.anchorTools(tools, root)
	if tools["t1"].Files[0].Hunks[0].NewStart != 0 {
		t.Error("read a file outside RCWEB_ROOT")
	}
}
