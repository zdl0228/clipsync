package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 开关项标题格式：标记在文字右侧，且不同长度的标签补齐到同一宽度，
// 这样三个 ✓ 在菜单里竖着对齐。
func TestToggleTitleAlignsMark(t *testing.T) {
	labels := []string{"自动发现广播", "右键发送文件", "开机启动"}

	onWidth, offWidth := -1, -1
	for _, label := range labels {
		on := toggleTitle(label, true)
		off := toggleTitle(label, false)

		if !strings.HasPrefix(on, label) {
			t.Errorf("%q 开启态标题 %q 没有以标签开头", label, on)
		}
		if !strings.HasSuffix(on, toggleMark) {
			t.Errorf("%q 开启态标题 %q 没有以勾选标记结尾", label, on)
		}
		if strings.Contains(off, toggleMark) {
			t.Errorf("%q 关闭态标题 %q 不应包含勾选标记", label, off)
		}

		w := utf8.RuneCountInString(on)
		if onWidth < 0 {
			onWidth = w
		} else if w != onWidth {
			t.Errorf("%q 开启态标题宽度 %d，与其它项 %d 不一致（勾选标记不会对齐）", label, w, onWidth)
		}

		w = utf8.RuneCountInString(off)
		if offWidth < 0 {
			offWidth = w
		} else if w != offWidth {
			t.Errorf("%q 关闭态标题宽度 %d，与其它项 %d 不一致", label, w, offWidth)
		}
	}
}
