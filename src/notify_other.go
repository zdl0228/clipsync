//go:build !darwin

package main

import (
	_ "embed"

	"github.com/gen2brain/beeep"
)

// win_notify.png 是放大剪贴板版图标，用作 Windows 通知头像
//
//go:embed win_notify.png
var winNotifyIcon []byte

// notifyOS 非 macOS 平台（Windows 等）用 beeep；
// Windows 10/11 走原生 Toast（WinRT COM API），图标用放大剪贴板版
func notifyOS(title, msg string) error {
	return beeep.Notify(title, msg, winNotifyIcon)
}

// notifyPrepare Windows 的 Toast 不需要预先授权
func notifyPrepare() {}

// notifyPermission Windows 没有可查询的单点授权状态，返回空表示无需展示
func notifyPermission() string { return "" }

// openNotificationSettings 非 macOS 平台没有统一的"通知设置"深链
func openNotificationSettings() {}
