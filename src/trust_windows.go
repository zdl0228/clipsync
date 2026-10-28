//go:build windows

package main

import "github.com/sqweek/dialog"

// confirmPeer 弹 Messagebox 询问是否允许某设备接入；返回用户是否选择「是」。
// Windows 下 Win32 MessageBox 是模态阻塞调用，可直接在 readLoop 的 goroutine 里用。
func confirmPeer(name, ip string) bool {
	return dialog.
		Message("设备「%s」（%s）请求连接并同步剪贴板。\n\n如果不是你自己的设备，请点「否」。", name, ip).
		Title("ClipSync 新设备接入").
		YesNo()
}
