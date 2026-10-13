//go:build !windows

package main

// macOS 暂不提供资源管理器右键菜单（与 Windows 注册表机制不同）。
// 以下占位仅保证跨平台编译；主程序按 isContextMenuEnabled() 判断，
// 在非 Windows 平台恒为 false，不注册任何菜单项。

// enableContextMenu macOS 占位：不做任何操作
func enableContextMenu() error { return nil }

// disableContextMenu macOS 占位：不做任何操作
func disableContextMenu() error { return nil }

// isContextMenuEnabled macOS 恒为 false
func isContextMenuEnabled() bool { return false }
