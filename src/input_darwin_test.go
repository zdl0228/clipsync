//go:build darwin

package main

import (
	"strings"
	"testing"
)

// 这条用例锁住"多设备右键发送永远取消"那个 bug：
// osascript 把列表输出成 "A (1.2.3.4), B (5.6.7.8)"（不带引号），
// 按引号切分必然解析不到目标。现在脚本自己用 ASCII 30 拼接。
func TestParsePeerSelection(t *testing.T) {
	cases := map[string][]string{
		"A (1.2.3.4)\x1eB (5.6.7.8)\x1e\n": {"A (1.2.3.4)", "B (5.6.7.8)"},
		"only (10.0.0.1)\x1e":              {"only (10.0.0.1)"},
		"":                                 nil,
		"\x1e\x1e":                         nil,
		// 设备名里带逗号和引号也不能切错（这是引号解析方案必然出错的地方）
		`B, Inc "x" (5.6.7.8)` + "\x1e": {`B, Inc "x" (5.6.7.8)`},
	}
	for in, want := range cases {
		got := parsePeerSelection(in)
		if len(got) != len(want) {
			t.Fatalf("parsePeerSelection(%q) = %q, want %q", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("parsePeerSelection(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// 生成的脚本要能正确处理名字里的引号，并且真的用 ASCII 30 拼接
func TestChoosePeersScript(t *testing.T) {
	s := choosePeersScript([]string{`A "quoted" (1.2.3.4)`, `B\path (5.6.7.8)`})
	if !strings.Contains(s, "ASCII character 30") {
		t.Fatal("脚本里没用 ASCII 30 分隔结果，解析会退化成按逗号切")
	}
	if !strings.Contains(s, `\"quoted\"`) {
		t.Fatalf("双引号没有转义: %s", s)
	}
	if !strings.Contains(s, `B\\path`) {
		t.Fatalf("反斜杠没有转义: %s", s)
	}
	// 每个设备都要出现在 devs 列表里，且都带引号
	if strings.Count(s, `"`) < 6 {
		t.Fatalf("列表项没有被正确加引号: %s", s)
	}
}
