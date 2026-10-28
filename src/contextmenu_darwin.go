//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// macOS 没有 Windows 那种"写注册表就能在任意位置加菜单项"的机制，
// 资源管理器右键菜单属于 Finder 扩展（FinderSync），要 app extension + 签名，
// 装不起来也不便分发。因此 mac 端的"右键发送"走 Finder 的**服务/快速操作**：
//
//	~/Library/Services/SendToClipSync.workflow  ← 一个 Automator workflow 包
//	   ├─ Contents/Info.plist       声明 NSServices（菜单文字、接受的类型）
//	   └─ Contents/[Resources/]document.wflow  一个 "Run Shell Script" 动作
//
// 脚本内容就是调用本程序：ClipSync --send "<选中的路径>"，主进程通过本地 IPC
// （127.0.0.1:2720）接手，再按在线设备发送。
//
// 用户侧路径：选中文件/文件夹 → 右键 → 服务（旧系统）/ 快速操作（Ventura+）
// →「发送到 ClipSync」。若不显示，见 开发手册.md「Finder 服务不出现怎么办」。

const (
	ctxMenuLabel = "Finder 右键发送"
	// 目录名不能沿用 SendToClipSync.workflow：pbs 以「目录路径」为 key 缓存服务
	// 条目，同路径重装不刷新内容，旧条目（带 NSIconName 的第一版）永远钉在缓存里。
	// 换目录名 + 换 bundle ID 才强制了重新注册。
	ctxServiceName = "ClipSyncSend.workflow"
	ctxServiceID   = "com.clipsync.send.v2"
	ctxServiceMenu = "发送到 ClipSync"
)

// ctxWorkflowPath 返回 workflow 包路径（用户级：~/Library/Services）
func ctxWorkflowPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("取不到用户主目录: %v", err)
	}
	return filepath.Join(home, "Library", "Services", ctxServiceName), nil
}

// xmlEscape 转义 XML 文本节点（& 必须先替换）
func xmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	).Replace(s)
}

// shQuote 把路径包成 POSIX shell 双引号字面量
func shQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// serviceInfoPlist 生成 workflow 的 Info.plist：
// NSSendFileTypes=public.item 让 Finder 把文件和文件夹都作为文件 URL 送进来；
// NSRequiredContext 限定只在 Finder 里出现，避免出现在别的 App 的"服务"菜单。
func serviceInfoPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleIdentifier</key>
	<string>` + ctxServiceID + `</string>
	<key>CFBundleName</key>
	<string>SendToClipSync</string>
	<key>CFBundleShortVersionString</key>
	<string>1.0</string>
	<key>NSServices</key>
	<array>
		<dict>
			<key>NSMenuItem</key>
			<dict>
				<key>default</key>
				<string>` + xmlEscape(ctxServiceMenu) + `</string>
			</dict>
			<key>NSMessage</key>
			<string>runWorkflowAsService</string>
			<key>NSSendFileTypes</key>
			<array>
				<string>public.item</string>
			</array>
		</dict>
	</array>
</dict>
</plist>
`
}

// serviceWorkflowDoc 生成 document.wflow：单个 "Run Shell Script" 动作，
// inputMethod=1 表示"把输入作为参数传给脚本"（$@ 即选中的路径列表）。
// 动作的键名/版本照 macOS 自带 workflow（Show Map.workflow）里的同一动作抄，
// 漏键会让 Automator 认不出该动作。
func serviceWorkflowDoc(exe string) string {
	cmd := "exec " + shQuote(exe) + ` --send "$@"`
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AMApplicationBuild</key>
	<string>521</string>
	<key>AMApplicationVersion</key>
	<string>2.10</string>
	<key>AMDocumentVersion</key>
	<string>2</string>
	<key>actions</key>
	<array>
		<dict>
			<key>action</key>
			<dict>
				<key>AMAccepts</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Optional</key>
					<true/>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>AMActionVersion</key>
				<string>2.0.3</string>
				<key>AMApplication</key>
				<array>
					<string>Automator</string>
				</array>
				<key>AMParameterProperties</key>
				<dict>
					<key>COMMAND_STRING</key>
					<dict/>
					<key>CheckedForUserDefaultShell</key>
					<dict/>
					<key>inputMethod</key>
					<dict/>
					<key>shell</key>
					<dict/>
					<key>source</key>
					<dict/>
				</dict>
				<key>AMProvides</key>
				<dict>
					<key>Container</key>
					<string>List</string>
					<key>Types</key>
					<array>
						<string>com.apple.cocoa.string</string>
					</array>
				</dict>
				<key>ActionBundlePath</key>
				<string>/System/Library/Automator/Run Shell Script.action</string>
				<key>ActionName</key>
				<string>Run Shell Script</string>
				<key>ActionParameters</key>
				<dict>
					<key>COMMAND_STRING</key>
					<string>` + xmlEscape(cmd) + `</string>
					<key>CheckedForUserDefaultShell</key>
					<true/>
					<key>inputMethod</key>
					<integer>1</integer>
					<key>shell</key>
					<string>/bin/sh</string>
					<key>source</key>
					<string></string>
				</dict>
				<key>BundleIdentifier</key>
				<string>com.apple.RunShellScript</string>
				<key>CFBundleVersion</key>
				<string>2.0.3</string>
				<key>CanShowSelectedItemsWhenRun</key>
				<false/>
				<key>CanShowWhenRun</key>
				<true/>
				<key>Category</key>
				<array>
					<string>AMCategoryUtilities</string>
				</array>
				<key>Class Name</key>
				<string>RunShellScriptAction</string>
				<key>InputUUID</key>
				<string>1A1B2C3D-0000-4000-8000-000000000001</string>
				<key>Keywords</key>
				<array>
					<string>Shell</string>
					<string>Script</string>
					<string>Command</string>
					<string>Run</string>
					<string>Unix</string>
				</array>
				<key>OutputUUID</key>
				<string>1A1B2C3D-0000-4000-8000-000000000002</string>
				<key>UUID</key>
				<string>1A1B2C3D-0000-4000-8000-000000000003</string>
				<key>UnlocalizedApplications</key>
				<array>
					<string>Automator</string>
				</array>
				<key>arguments</key>
				<dict/>
				<key>isViewVisible</key>
				<true/>
				<key>location</key>
				<string>309.000000:253.000000</string>
				<key>nibPath</key>
				<string>/System/Library/Automator/Run Shell Script.action/Contents/Resources/Base.lproj/main.nib</string>
			</dict>
			<key>isViewVisible</key>
			<true/>
		</dict>
	</array>
	<key>connectors</key>
	<dict/>
	<key>workflowMetaData</key>
	<dict>
		<key>serviceApplicationBundleID</key>
		<string></string>
		<key>serviceApplicationPath</key>
		<string></string>
		<key>serviceInputTypeIdentifier</key>
		<string>com.apple.Automator.fileSystemObject</string>
		<key>serviceOutputTypeIdentifier</key>
		<string>com.apple.Automator.nothing</string>
		<key>serviceProcessesInput</key>
		<integer>0</integer>
		<key>workflowTypeIdentifier</key>
		<string>com.apple.Automator.servicesMenu</string>
	</dict>
	<key>workflowUUID</key>
	<string>1A1B2C3D-0000-4000-8000-000000000004</string>
</dict>
</plist>
`
}

// refreshServices 让新装的 workflow 立刻出现在"服务"菜单里。
// pbs（粘贴板服务器）自己会扫描 ~/Library/Services 并重建服务列表，实测装完
// 不用重启 Finder 就能用；不要顺手调 lsregister —— 它对 .workflow 这种
// 非 app 包返回 -10811（kLSNotAnApplicationErr），每次启动都会在日志里刷错误。
// ensureServiceEnabled macOS 默认把**所有第三方服务**的右键菜单项关掉：
// pbs 的 NSServicesStatus 里每个非 Apple 服务都带 enabled_context_menu = false，
// 用户在「系统设置 → 键盘 → 键盘快捷键 → 服务」勾选后才会变成 true。
// 这一步不做，workflow 装得再对、pbs 也认得，右键「服务」里照样什么都没有
// （2026-10-28 那次"服务里面没有"的真因）。
//
// 改法上别用 `defaults write -dict-add`：它会把 1 写成字符串 "1"，
// 这里要的是布尔。走 export → JSON → 改 → import，全程不碰 plist 语法。
// 返回 true 表示找到了对应条目并已启用。
func ensureServiceEnabled() bool {
	dir, err := os.MkdirTemp("", "clipsync-pbs")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "pbs.plist")
	js := filepath.Join(dir, "pbs.json")
	out := filepath.Join(dir, "pbs.out.plist")

	// pbs 域还不存在时 export 会失败，此时没什么可改的
	if err := exec.Command("/usr/bin/defaults", "export", "pbs", src).Run(); err != nil {
		return false
	}
	if err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", js, src).Run(); err != nil {
		return false
	}
	b, err := os.ReadFile(js)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return false
	}
	status, _ := doc["NSServicesStatus"].(map[string]any)
	if status == nil {
		return false
	}

	// 键形如 "<bundle id 或 (null)> - <菜单名> - runWorkflowAsService"，
	// 菜单名在键里会被规范化（实测空格被吃掉），所以只按 ClipSync 匹配
	found, changed := false, false
	for k, v := range status {
		if !strings.Contains(k, "ClipSync") || !strings.Contains(k, "runWorkflowAsService") {
			continue
		}
		found = true
		m, ok := v.(map[string]any)
		if !ok {
			m = map[string]any{}
			status[k] = m
		}
		if m["enabled_context_menu"] == true && m["enabled_services_menu"] == true {
			continue
		}
		m["enabled_context_menu"] = true
		m["enabled_services_menu"] = true
		m["presentation_modes"] = map[string]any{
			"ContextMenu": true, "ServicesMenu": true, "TouchBar": true,
		}
		changed = true
	}
	if !found || !changed {
		return found
	}
	nb, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false
	}
	if err := os.WriteFile(js, nb, 0600); err != nil {
		return false
	}
	if err := exec.Command("/usr/bin/plutil", "-convert", "binary1", "-o", out, js).Run(); err != nil {
		return false
	}
	if err := exec.Command("/usr/bin/defaults", "import", "pbs", out).Run(); err != nil {
		return false
	}
	log.Println("已在系统服务列表里启用「" + ctxServiceMenu + "」")
	return true
}

func refreshServices() {
	runQuiet("/System/Library/CoreServices/pbs", "-update")
	// pbs 扫描目录是异步的，刚写进去的 workflow 可能还没进 NSServicesStatus，
	// 隔一会儿重试几次；这次没找到的话下次启动还会再来一遍
	found := false
	for i := 0; i < 10; i++ {
		if ensureServiceEnabled() {
			found = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !found {
		log.Println("pbs 里还看不到服务条目，稍后重启会自动再试")
	}
	// pbs 只重建自己的数据库，Finder 是启动时把服务菜单建好后缓存的，
	// 不重启就一直看不到新条目。只有内容真的变了才会走到这里，
	// 所以不会每次开机都重启 Finder。
	runQuiet("/usr/bin/killall", "Finder")
}

// ctxNeedsRewrite 比较磁盘上已有的 workflow 与新生成的内容。
// 一致就什么都不做：既省一次写盘，也避免每次启动都重启 Finder。
func ctxNeedsRewrite(wf string, plist, doc []byte) bool {
	if b, err := os.ReadFile(filepath.Join(wf, "Contents", "Info.plist")); err != nil || !bytes.Equal(b, plist) {
		return true
	}
	for _, p := range []string{
		filepath.Join("Contents", "document.wflow"),
		filepath.Join("Contents", "Resources", "document.wflow"),
	} {
		if b, err := os.ReadFile(filepath.Join(wf, p)); err != nil || !bytes.Equal(b, doc) {
			return true
		}
	}
	return false
}

// runQuiet 跑一个辅助命令，最多等 5 秒，失败只记日志（不影响主功能）
func runQuiet(name string, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		log.Printf("服务注册辅助命令失败(%s): %v %s", filepath.Base(name), err, strings.TrimSpace(string(out)))
	}
}

// enableContextMenu 安装（或刷新）Finder 服务；可重复调用：
// 每次都整体重写，顺带修掉"app 被挪了位置导致旧路径失效"的问题。
func enableContextMenu() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	wf, err := ctxWorkflowPath()
	if err != nil {
		return err
	}

	plist := []byte(serviceInfoPlist())
	doc := []byte(serviceWorkflowDoc(exe))
	if !ctxNeedsRewrite(wf, plist, doc) {
		return nil // 已是最新，别动它（重启 Finder 很打扰）
	}

	// 先写到 .tmp 再整体替换，避免写到一半被 Finder 读到残缺包
	tmp := wf + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	dirs := []string{
		filepath.Join(tmp, "Contents"),
		filepath.Join(tmp, "Contents", "Resources"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "Contents", "Info.plist"), plist, 0644); err != nil {
		return err
	}
	// document.wflow 的位置各版本不一（老的读 Contents/，系统自带的在
	// Contents/Resources/），两处都写一份，省得某个系统版本上点了没反应
	for _, p := range []string{
		filepath.Join(tmp, "Contents", "document.wflow"),
		filepath.Join(tmp, "Contents", "Resources", "document.wflow"),
	} {
		if err := os.WriteFile(p, doc, 0644); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(wf); err != nil {
		return err
	}
	if err := os.Rename(tmp, wf); err != nil {
		return err
	}
	log.Printf("已安装 Finder 服务: %s（指向 %s）", wf, exe)
	refreshServices()
	return nil
}

// disableContextMenu 移除 workflow 包；Finder 下次读取服务列表时就不见了
func disableContextMenu() error {
	wf, err := ctxWorkflowPath()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(wf); err != nil {
		return err
	}
	log.Printf("已移除 Finder 服务: %s", wf)
	return nil
}

// isContextMenuEnabled 服务是否已安装
func isContextMenuEnabled() bool {
	wf, err := ctxWorkflowPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(wf, "Contents", "Info.plist"))
	return err == nil
}

// contextMenuHint 开启成功后给用户的提示（Finder 服务藏在子菜单里，不说明找不到）
func contextMenuHint() string {
	return "已启用 Finder 服务。\n\n用法：选中文件或文件夹 → 右键 → 服务（或“快速操作”）→「发送到 ClipSync」。\n\n" +
		"如果菜单里暂时看不到，注销/重启 Finder 后就会出现在「系统设置 → 键盘 → 键盘快捷键 → 服务」里。"
}
