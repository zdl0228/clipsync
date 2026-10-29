package main

import (
	"strings"
	"unicode/utf8"
)

// toggleMark 开关项的勾选标记。
//
// 不用 systray 自带的 Check()（macOS 会在文字左边画勾选框）：那个勾选框只在
// 带状态的项上出现，「配对密钥…」「直连指定 IP…」这类普通项没有，于是两类项
// 的文字左边缘错开，菜单看着参差不齐。把标记并进标题文字末尾后，所有项文字
// 都从同一列起，勾选标记统一落在右侧。
const toggleMark = "✓"

// toggleLabelCells 开关项标签按全角字符计的统一宽度，取现有开关名里最长的
// 「自动发现广播」/「右键发送文件」（各 6 个字）。短标签补全角空格凑齐，
// 让三个勾选标记竖着排在同一列。
const toggleLabelCells = 6

// toggleTitle 生成开关项标题：开 = 文字 + 勾选标记，关 = 只有文字。
func toggleTitle(label string, on bool) string {
	if n := utf8.RuneCountInString(label); n < toggleLabelCells {
		label += strings.Repeat("\u3000", toggleLabelCells-n)
	}
	if on {
		return label + " " + toggleMark
	}
	return label
}
