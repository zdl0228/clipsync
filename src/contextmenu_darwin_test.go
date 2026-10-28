//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 临时测试：生成的 workflow 必须是合法 plist，且脚本能正确把带空格的路径传下去
func TestServiceWorkflowSmoke(t *testing.T) {
	dir := t.TempDir()

	// 1) 两个 plist 都要 plutil -lint 通过
	files := map[string]string{
		"Info.plist":     serviceInfoPlist(),
		"document.wflow": serviceWorkflowDoc("/Applications/ClipSync.app/Contents/MacOS/ClipSync"),
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("plutil", "-lint", p).CombinedOutput(); err != nil {
			t.Fatalf("%s 不是合法 plist: %v\n%s", name, err, out)
		}
	}

	// 2) 脚本里的 exe 路径要能保住空格（模拟 app 装在带空格的目录）
	fake := filepath.Join(dir, "fake exe.sh")
	out := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shQuote(out) + "\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// 复刻 workflow 里的命令：exec <exe> --send "$@"
	cmd := exec.Command("/bin/sh", "-c", "exec "+shQuote(fake)+` --send "$@"`, "sh", "/tmp/有 空格 的文件.txt", "/tmp/b.txt")
	if err := cmd.Run(); err != nil {
		t.Fatalf("脚本执行失败: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || lines[0] != "--send" || lines[1] != "/tmp/有 空格 的文件.txt" || lines[2] != "/tmp/b.txt" {
		t.Fatalf("参数传递不对: %q", lines)
	}
}
