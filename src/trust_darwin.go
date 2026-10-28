//go:build darwin

package main

import (
	"log"
	"os/exec"
	"strings"
)

// confirmPeer 弹系统对话框询问是否允许某设备接入；返回用户是否允许。
// 这里用 osascript 而不是 NSAlert：osascript 跑在独立进程里，从 readLoop 这类
// 后台 goroutine 调用完全安全，也与本项目其它对话框（inputIP / choosePeers）一致，
// 不必担心 AppKit 主线程限制。
func confirmPeer(name, ip string) bool {
	msg := "设备「" + name + "」（" + ip + "）请求连接并同步剪贴板。\n\n如果不是你自己的设备，请点「拒绝」。"
	script := `display dialog "` + escapeAppleScript(msg) + `" with title "ClipSync 新设备接入"` +
		` buttons {"拒绝", "允许"} default button "允许" with icon caution`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		// 点「拒绝」时 osascript 以非零状态退出，按拒绝处理
		log.Printf("接入确认：用户拒绝或对话框失败（%v）", err)
		return false
	}
	return strings.Contains(string(out), "允许")
}

// escapeAppleScript 转义 AppleScript 双引号字符串中的反斜杠与引号
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
