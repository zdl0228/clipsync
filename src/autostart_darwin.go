//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchAgentLabel = "com.local.clipsync"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

// launchAgentExists 登录项 plist 是否存在（只看文件，不判断系统是否禁用）
func launchAgentExists() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

// isDisabledBySystem launchd 是否已禁用该登录项。
// 用户在「系统设置 → 通用 → 登录项与扩展」里取消勾选时，macOS 只执行
// launchctl disable，**plist 文件仍留着**；只靠文件存在与否判断会把「已关闭」
// 误判成「开启」，造成菜单显示和真实状态不一致。
func isDisabledBySystem() bool {
	uid := fmt.Sprintf("%d", os.Getuid())
	out, err := exec.Command("launchctl", "print-disabled", "gui/"+uid).CombinedOutput()
	if err != nil {
		// 查询失败时保守按「未禁用」处理，避免误报成关闭
		return false
	}
	return strings.Contains(string(out), `"`+launchAgentLabel+`" => disabled`)
}

// isAutoStartEnabled 开机启动是否真正生效：plist 存在 **且** 未被系统禁用。
func isAutoStartEnabled() bool {
	return launchAgentExists() && !isDisabledBySystem()
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
	uid := fmt.Sprintf("%d", os.Getuid())
	// 解除系统层面的禁用标记（用户可能此前在系统设置里关过）。
	// 注意：只 enable、不 bootstrap，避免 RunAtLoad 当场再启动一个实例（双图标）；
	// 开机时由 launchd 自动加载该登录项。
	exec.Command("launchctl", "enable", "gui/"+uid+"/"+launchAgentLabel).Run()
	return nil
}

func disableAutoStart() error {
	path := launchAgentPath()
	uid := fmt.Sprintf("%d", os.Getuid())
	// 若此前 bootstrap 注册过，先卸载该登录项
	exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchAgentLabel).Run()
	// 同时打上 disabled 标记：即使 plist 因故残留，launchd 也不会再加载它
	exec.Command("launchctl", "disable", "gui/"+uid+"/"+launchAgentLabel).Run()
	os.Remove(path)
	return nil
}
