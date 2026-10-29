package main

import (
	"strings"
	"testing"
)

// 勾选标记紧跟文字、不加任何空白：菜单宽度由最长项决定，补白会把菜单撑宽。
func TestToggleTitleFollowsLabel(t *testing.T) {
	labels := []string{"自动发现广播", "右键发送文件", "开机启动"}

	for _, label := range labels {
		on := toggleTitle(label, true)
		off := toggleTitle(label, false)

		if on != label+toggleMark {
			t.Errorf("开启态 = %q，应为 %q", on, label+toggleMark)
		}
		if strings.ContainsAny(on, " \u3000\t") {
			t.Errorf("开启态 %q 不该有空白字符", on)
		}
		if off != label {
			t.Errorf("关闭态 = %q，应为 %q", off, label)
		}
	}
}
