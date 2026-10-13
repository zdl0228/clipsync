//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// 右键菜单注册表键。ClipSyncSend 为菜单标识名（可自定义显示文字）。
// - "*"        匹配所有文件
// - "Directory" 匹配文件夹
const (
	ctxMenuDisplay = "发送到 ClipSync"
)

// ctxMenuKeys 需要注册的右键菜单作用域
var ctxMenuKeys = []string{
	`*`,
	`Directory`,
}

// ctxShellKey 返回某作用域下 ClipSyncSend 菜单项的完整键路径
func ctxShellKey(scope string) string {
	return `Software\Classes\` + scope + `\shell\ClipSyncSend`
}

// enableContextMenu 写入注册表注册右键菜单；重复调用幂等（相同值覆盖无害）
func enableContextMenu() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 右键点击目标（%1）时启动同一 exe 的发送模式：--send "<路径>"
	cmdLine := `"` + exe + `" --send "%1"`
	for _, scope := range ctxMenuKeys {
		key, _, err := registry.CreateKey(registry.CURRENT_USER, ctxShellKey(scope), registry.SET_VALUE)
		if err != nil {
			return err
		}
		// 默认值：菜单显示文字
		key.SetStringValue("", ctxMenuDisplay)
		// 图标：复用主程序图标
		key.SetStringValue("Icon", `"`+exe+`"`)
		key.Close()

		cmdKey, _, err := registry.CreateKey(registry.CURRENT_USER, ctxShellKey(scope)+`\command`, registry.SET_VALUE)
		if err != nil {
			return err
		}
		cmdKey.SetStringValue("", cmdLine)
		cmdKey.Close()
	}
	return nil
}

// disableContextMenu 删除右键菜单注册表项
func disableContextMenu() error {
	for _, scope := range ctxMenuKeys {
		// 递归删除整个 ClipSyncSend 子树（含 command 子键）
		err := registry.DeleteKey(registry.CURRENT_USER, ctxShellKey(scope))
		if err != nil && err != registry.ErrNotExist {
			return err
		}
	}
	return nil
}

// isContextMenuEnabled 右键菜单是否已注册
func isContextMenuEnabled() bool {
	// 判断 `*\shell\ClipSyncSend` 是否存在即可（幂等注册，其余作用域一致）
	k, err := registry.OpenKey(registry.CURRENT_USER, ctxShellKey(`*`), registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}
