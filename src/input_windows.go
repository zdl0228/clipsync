//go:build windows

package main

import (
	"log"
	"os/exec"
	"strings"
	"syscall"
)

// psQuote 把文本包成 PowerShell 单引号字符串（内部单引号双写）
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// inputText 弹出输入框，返回用户输入（默认值预填）；取消返回 ""。
// 用 WinForms 而非 VisualBasic InputBox：后者宽度写死、长密钥显示不全，
// 也不能可靠预填。窗口在 -STA 下才能稳定弹出。
func inputText(title, prompt, def string) string {
	ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n" +
		"Add-Type -AssemblyName System.Windows.Forms\n" +
		"Add-Type -AssemblyName System.Drawing\n" +
		"$form = New-Object System.Windows.Forms.Form\n" +
		"$form.Text = " + psQuote(title) + "\n" +
		"$form.StartPosition = 'CenterScreen'\n" +
		"$form.FormBorderStyle = 'FixedDialog'\n" +
		"$form.MinimizeBox = $false\n" +
		"$form.MaximizeBox = $false\n" +
		"$form.Size = New-Object System.Drawing.Size(470, 200)\n" +
		"$label = New-Object System.Windows.Forms.Label\n" +
		"$label.Location = New-Object System.Drawing.Point(14, 12)\n" +
		"$label.Size = New-Object System.Drawing.Size(430, 38)\n" +
		"$label.Text = " + psQuote(prompt) + "\n" +
		"$box = New-Object System.Windows.Forms.TextBox\n" +
		"$box.Location = New-Object System.Drawing.Point(14, 56)\n" +
		"$box.Size = New-Object System.Drawing.Size(430, 24)\n" +
		"$box.Text = " + psQuote(def) + "\n" +
		"$ok = New-Object System.Windows.Forms.Button\n" +
		"$ok.Text = '确定'\n" +
		"$ok.Location = New-Object System.Drawing.Point(270, 100)\n" +
		"$ok.DialogResult = [System.Windows.Forms.DialogResult]::OK\n" +
		"$cancel = New-Object System.Windows.Forms.Button\n" +
		"$cancel.Text = '取消'\n" +
		"$cancel.Location = New-Object System.Drawing.Point(360, 100)\n" +
		"$cancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel\n" +
		"$form.Controls.Add($label)\n" +
		"$form.Controls.Add($box)\n" +
		"$form.Controls.Add($ok)\n" +
		"$form.Controls.Add($cancel)\n" +
		"$form.AcceptButton = $ok\n" +
		"$form.CancelButton = $cancel\n" +
		"$form.Add_Shown({ $box.SelectAll(); $box.Focus() })\n" +
		"if ($form.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { $box.Text } else { '' }"
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		log.Printf("输入框执行失败: %v", err)
		return ""
	}
	s := strings.TrimSpace(string(out))
	return strings.TrimPrefix(s, "\uFEFF") // 去掉可能的 BOM
}

// inputIP 弹出输入框获取对端直连 IP；取消或为空返回 ""
func inputIP() string {
	return strings.TrimSpace(inputText("ClipSync 直连", "输入要对端直连的 IP 地址（如 192.168.1.100）", ""))
}

// selectPaths 弹出系统原生多选文件对话框；取消返回 nil
// 说明：Windows 用 WinForms OpenFileDialog（由本机可靠弹出）多选文件；
// 发送整个文件夹建议在 Mac 端操作（Mac 端支持文件和文件夹一起多选）。
func selectPaths() []string {
	log.Println("请求弹出文件选择面板")
	// -STA 让 WinForms 对话框可靠弹出；强制 UTF-8 输出，避免中文文件名路径损坏
	ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n" +
		"Add-Type -AssemblyName System.Windows.Forms\n" +
		"$d = New-Object System.Windows.Forms.OpenFileDialog\n" +
		"$d.Title = \"选择要发送的文件（可多选）\"\n" +
		"$d.Multiselect = $true\n" +
		"$d.RestoreDirectory = $true\n" +
		"$r = $d.ShowDialog()\n" +
		"if ($r -eq [System.Windows.Forms.DialogResult]::OK) { $d.FileNames -join \"`n\" } else { \"\" }"
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		log.Printf("文件选择面板执行失败: %v", err)
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "\uFEFF") // 去掉可能的 BOM
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		log.Println("文件选择面板未返回结果（可能用户取消）")
		return nil
	}
	log.Printf("文件选择面板返回 %d 个路径", len(paths))
	return paths
}

// choosePeers 弹出多选对话框，让用户勾选要发送的目标设备（可多选）；
// opts 形如 "设备名 (IP)"，返回选中的条目（每行一条），取消返回 nil。
// 用 WinForms CheckedListBox，-STA 模式确保对话框可靠弹出。
func choosePeers(opts []string) []string {
	if len(opts) == 0 {
		return nil
	}
	// 单引号字符串转义：设备名中若含单引号则双写
	var quoted []string
	for _, o := range opts {
		quoted = append(quoted, "'"+strings.ReplaceAll(o, "'", "''")+"'")
	}
	devs := strings.Join(quoted, ", ")
	// 脚本不含反引号字符，用 raw string 书写；换行用 [char]10 避免转义符
	ps := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n" +
		"Add-Type -AssemblyName System.Windows.Forms\n" +
		"Add-Type -AssemblyName System.Drawing\n" +
		"$form = New-Object System.Windows.Forms.Form\n" +
		"$form.Text = \"ClipSync - 选择发送目标设备（可多选）\"\n" +
		"$form.StartPosition = 'CenterScreen'\n" +
		"$form.Size = New-Object System.Drawing.Size(380, 450)\n" +
		"$form.MinimizeBox = $false\n" +
		"$form.MaximizeBox = $false\n" +
		"$list = New-Object System.Windows.Forms.CheckedListBox\n" +
		"$list.Location = New-Object System.Drawing.Point(12, 12)\n" +
		"$list.Size = New-Object System.Drawing.Size(348, 300)\n" +
		"$devs = @(" + devs + ")\n" +
		"foreach ($d in $devs) { [void]$list.Items.Add($d) }\n" +
		"$form.Controls.Add($list)\n" +
		"$ok = New-Object System.Windows.Forms.Button\n" +
		"$ok.Text = '发送'\n" +
		"$ok.Location = New-Object System.Drawing.Point(180, 330)\n" +
		"$ok.DialogResult = [System.Windows.Forms.DialogResult]::OK\n" +
		"$form.Controls.Add($ok)\n" +
		"$cancel = New-Object System.Windows.Forms.Button\n" +
		"$cancel.Text = '取消'\n" +
		"$cancel.Location = New-Object System.Drawing.Point(270, 330)\n" +
		"$cancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel\n" +
		"$form.Controls.Add($cancel)\n" +
		"$form.AcceptButton = $ok\n" +
		"$form.CancelButton = $cancel\n" +
		"if ($form.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {\n" +
		"  $sel = @($list.CheckedItems)\n" +
		"  if ($sel.Count -gt 0) { $sel -join ([string][char]10) } else { '' }\n" +
		"} else { '' }"
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		log.Printf("目标设备选择框执行失败: %v", err)
		return nil
	}
	var res []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "\uFEFF") // 去掉可能的 BOM
		if line != "" {
			res = append(res, line)
		}
	}
	if len(res) == 0 {
		log.Println("目标设备选择框：未选择，取消")
		return nil
	}
	log.Printf("目标设备选择框返回 %d 台", len(res))
	return res
}
