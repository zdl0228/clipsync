//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "ClipSync"
)

// launchAgentExists Windows 下等价于「Run 注册表值是否存在」。
// mac 用 LaunchAgent plist 文件，两边名称统一，供 main.go 共用。
func launchAgentExists() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

// isAutoStartEnabled 是否已配置开机启动（注册表 Run 项是否存在）
func isAutoStartEnabled() bool {
	return launchAgentExists()
}

// setAutoStart 设置或取消开机启动
func setAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue(runValue, `"`+exe+`"`)
	}
	return k.DeleteValue(runValue)
}
