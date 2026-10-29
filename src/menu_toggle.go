package main

// toggleMark 开关项的勾选标记，紧跟在文字后面（不补空格、不对齐列）。
//
// 不用 systray 自带的 Check()：macOS 会在文字左边画勾选框，而只有带状态的项
// 才占那一列，「配对密钥」这类普通项不缩进，两类项的文字左边缘就错开了。
// 把标记并进标题文字末尾后，所有项都从同一列起头，勾在右侧跟着文字走。
const toggleMark = "✓"

// toggleTitle 生成开关项标题：开 = 文字 + 勾选标记，关 = 只有文字。
func toggleTitle(label string, on bool) string {
	if on {
		return label + toggleMark
	}
	return label
}
