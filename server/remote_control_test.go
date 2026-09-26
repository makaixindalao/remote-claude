package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRCScript(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: &Config{Root: dir, ClaudeBin: "claude"}, settings: newSettingsStore(dir)}

	rc := RemoteControl{Name: "it's mine", PermissionMode: "acceptEdits", Spawn: "worktree", Capacity: 4, AutoRestart: true, RestartDelay: 7}
	got := s.rcScript(rc, dir)
	for _, want := range []string{
		"cd " + shq(dir) + " || exit 1",
		`'claude' remote-control --name 'it'\''s mine' --permission-mode acceptEdits --spawn worktree --capacity 4`,
		"while :; do", "sleep 7", "退出（${code}）",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("脚本里没有 %q:\n%s", want, got)
		}
	}
	// 默认值不写进命令行，跟着 claude 自己的默认走
	once := s.rcScript(defaultRC, dir)
	if strings.Contains(once, "--permission-mode") || strings.Contains(once, "--spawn") || strings.Contains(once, "--capacity") {
		t.Errorf("默认值不该出现在命令行里:\n%s", once)
	}
	noRestart := defaultRC
	noRestart.AutoRestart = false
	if got := s.rcScript(noRestart, dir); !strings.Contains(got, "exec 'claude' remote-control\n") || strings.Contains(got, "while") {
		t.Errorf("不自动重启时应该直接 exec:\n%s", got)
	}

	// 生成的脚本得是合法的 sh
	for _, body := range []string{got, once} {
		path := filepath.Join(dir, "rc.sh")
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("sh -n: %v %s\n%s", err, out, body)
		}
	}
}

func TestValidateRC(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: &Config{Root: dir}, settings: newSettingsStore(dir)}
	rc := RemoteControl{Enabled: true}
	if err := s.validateRC(&rc); err != nil {
		t.Fatal(err)
	}
	if rc.PermissionMode != "default" || rc.Spawn != "same-dir" || rc.RestartDelay != defaultRC.RestartDelay {
		t.Fatalf("没补默认值: %+v", rc)
	}
	for _, bad := range []RemoteControl{
		{PermissionMode: "yolo"},
		{Spawn: "--help"},
		{Capacity: -1},
		{Name: "a\nb"},
		{Name: strings.Repeat("名", 65)},
		{Enabled: true, Project: "no-such-project"},
	} {
		if err := s.validateRC(&bad); err == nil {
			t.Errorf("%+v 应该不合法", bad)
		}
	}
	// 项目没了也要能关掉
	if err := s.validateRC(&RemoteControl{Project: "no-such-project"}); err != nil {
		t.Errorf("关掉时不该查目录: %v", err)
	}
}
