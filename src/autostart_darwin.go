//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const launchAgentLabel = "com.local.clipsync"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

// isAutoStartEnabled 是否已配置开机启动（登录项是否存在）
func isAutoStartEnabled() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

// setAutoStart 设置或取消开机启动
func setAutoStart(enable bool) error {
	if enable {
		return enableAutoStart()
	}
	return disableAutoStart()
}

func enableAutoStart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchAgentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + exe + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>`
	path := launchAgentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		return err
	}
	// 注意：不调用 launchctl bootstrap，避免 RunAtLoad 当场再启动一个实例（出现双图标）。
	// 开机自启由下次登录时 launchd 自动加载该登录项触发。
	return nil
}

func disableAutoStart() error {
	path := launchAgentPath()
	uid := fmt.Sprintf("%d", os.Getuid())
	// 若此前 bootstrap 注册过，先卸载该登录项
	exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()
	os.Remove(path)
	return nil
}
