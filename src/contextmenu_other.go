//go:build !windows && !darwin

package main

// 其它平台（linux 等）没有资源管理器右键菜单，以下占位保证跨平台编译。
// macOS 另有实现，见 contextmenu_darwin.go（Finder 服务）。

const ctxMenuLabel = "右键发送"

// enableContextMenu 占位：不做任何操作
func enableContextMenu() error { return nil }

// disableContextMenu 占位：不做任何操作
func disableContextMenu() error { return nil }

// isContextMenuEnabled 恒为 false
func isContextMenuEnabled() bool { return false }

// contextMenuHint 无提示
func contextMenuHint() string { return "" }
