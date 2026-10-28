//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/sqweek/dialog"
	"github.com/TheTitanrain/w32"
)

// createNoWindow 让子进程不创建控制台窗口（CREATE_NO_WINDOW）。
//
// 这里不能用 SysProcAttr.HideWindow：它设置的是 STARTUPINFO.wShowWindow = SW_HIDE，
// 而 WinForms 窗体首次显示走 SW_SHOWDEFAULT，会继承这个值 —— 于是 powershell
// 进程起来了、脚本也执行了，窗口却完全不可见。用户看到的就是"点了没反应"。
// CREATE_NO_WINDOW 只是不分配控制台，不影响后续自建窗体，是隐藏控制台的正确姿势。
const createNoWindow = 0x08000000

// psRun 以 STA 模式执行 PowerShell 脚本并返回 UTF-8 标准输出（已去首尾空白与 BOM）。
// 失败时把 stderr 一并带出来，否则只看 "exit status 1" 根本不知道脚本错在哪。
func psRun(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	s := strings.TrimLeft(string(out), "\uFEFF")
	return strings.TrimSpace(s), nil
}

// psQuote 把文本包成 PowerShell 单引号字符串（内部单引号双写）
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ---- 纯 Win32 输入框（替代 PowerShell+WinForms）----
//
// 旧实现每次弹窗都要冷启动一个 powershell.exe 进程，再加载
// System.Windows.Forms / System.Drawing 程序集，点"设置密钥"要等 1~3 秒。
// 这里改为直接调 Win32 创建窗口，弹出是瞬时的。

const inputBoxClass = "ClipSync.InputBox"

// 输入框控件 ID
const (
	idcInputLabel = 100
	idcInputEdit  = 101
)

var (
	inputBoxOnce sync.Once
	inputEditHwnd w32.HWND
	inputResult   string
)

// inputWndProc 输入框主窗口的窗口过程。回调运行在系统线程上，只做轻量纯 Win32
// 调用（读编辑框文本、销毁窗口、退出消息循环），不阻塞，安全。
func inputWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	h := w32.HWND(hwnd)
	switch msg {
	case w32.WM_COMMAND:
		switch w32.LOWORD(uint32(wParam)) {
		case uint16(w32.IDOK):
			inputResult = w32.GetWindowText(inputEditHwnd)
			w32.DestroyWindow(h)
			return 0
		case uint16(w32.IDCANCEL):
			inputResult = ""
			w32.DestroyWindow(h)
			return 0
		}
	case w32.WM_CLOSE:
		w32.DestroyWindow(h)
		return 0
	case w32.WM_DESTROY:
		w32.PostQuitMessage(0)
		return 0
	}
	return w32.DefWindowProc(h, msg, wParam, lParam)
}

// registerInputBoxClass 注册输入框窗口类（进程内只注册一次）
func registerInputBoxClass() {
	inst := w32.GetModuleHandle("")
	wc := w32.WNDCLASSEX{
		Size:       uint32(unsafe.Sizeof(w32.WNDCLASSEX{})),
		Style:      w32.CS_HREDRAW | w32.CS_VREDRAW,
		WndProc:    syscall.NewCallback(inputWndProc),
		Instance:   inst,
		Cursor:     w32.LoadCursor(0, w32.MakeIntResource(uint16(w32.IDC_ARROW))),
		Background: w32.HBRUSH(w32.COLOR_BTNFACE + 1), // 系统对话框底色
		ClassName:  syscall.StringToUTF16Ptr(inputBoxClass),
	}
	w32.RegisterClassEx(&wc)
}

// wrapText 把长文本按固定字符数折行，供提示标签多行显示
func wrapText(s string, width int) string {
	rs := []rune(s)
	if len(rs) <= width {
		return s
	}
	var b strings.Builder
	for i, r := range rs {
		if i > 0 && i%width == 0 {
			b.WriteByte('\n')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// nativeInputText 弹输入框并阻塞到用户确定/取消。确定返回编辑框内容，取消返回 ""。
func nativeInputText(title, prompt, def string) string {
	inputResult = ""
	inputEditHwnd = 0

	inputBoxOnce.Do(registerInputBoxClass)

	inst := w32.GetModuleHandle("")
	style := uint(w32.WS_OVERLAPPED | w32.WS_CAPTION | w32.WS_SYSMENU)

	// 客户区按 480x220 设计，用 AdjustWindowRect 换算成含标题栏/边框的窗口尺寸
	rc := w32.RECT{Right: 480, Bottom: 220}
	w32.AdjustWindowRect(&rc, style, false)
	width := int(rc.Right - rc.Left)
	height := int(rc.Bottom - rc.Top)

	// 屏幕居中
	x := (w32.GetSystemMetrics(w32.SM_CXSCREEN) - width) / 2
	y := (w32.GetSystemMetrics(w32.SM_CYSCREEN) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	hwnd := w32.CreateWindowEx(
		uint(w32.WS_EX_TOPMOST),
		syscall.StringToUTF16Ptr(inputBoxClass),
		syscall.StringToUTF16Ptr(title),
		style,
		x, y, width, height,
		0, 0, inst, nil)
	if hwnd == 0 {
		log.Printf("输入框创建失败")
		return ""
	}

	labelHwnd := w32.CreateWindowEx(0,
		syscall.StringToUTF16Ptr("STATIC"),
		syscall.StringToUTF16Ptr(wrapText(prompt, 30)),
		uint(w32.WS_CHILD|w32.WS_VISIBLE|w32.SS_LEFT),
		14, 12, 452, 56, hwnd, w32.HMENU(idcInputLabel), inst, nil)

	editHwnd := w32.CreateWindowEx(
		uint(w32.WS_EX_CLIENTEDGE),
		syscall.StringToUTF16Ptr("EDIT"),
		nil,
		uint(w32.WS_CHILD|w32.WS_VISIBLE|w32.WS_TABSTOP|w32.ES_AUTOHSCROLL|w32.ES_LEFT),
		14, 72, 452, 24, hwnd, w32.HMENU(idcInputEdit), inst, nil)
	inputEditHwnd = editHwnd
	w32.SetWindowText(editHwnd, def)
	w32.SendMessage(editHwnd, w32.EM_SETSEL, 0, ^uintptr(0)) // 全选，方便直接覆盖

	okHwnd := w32.CreateWindowEx(0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("确定"),
		uint(w32.WS_CHILD|w32.WS_VISIBLE|w32.WS_TABSTOP|w32.BS_DEFPUSHBUTTON),
		280, 118, 84, 28, hwnd, w32.HMENU(w32.IDOK), inst, nil)
	cancelHwnd := w32.CreateWindowEx(0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("取消"),
		uint(w32.WS_CHILD|w32.WS_VISIBLE|w32.WS_TABSTOP|w32.BS_PUSHBUTTON),
		372, 118, 84, 28, hwnd, w32.HMENU(w32.IDCANCEL), inst, nil)

	// 统一系统 GUI 字体（与标准对话框一致）
	if font := w32.GetStockObject(w32.DEFAULT_GUI_FONT); font != 0 {
		for _, c := range []w32.HWND{labelHwnd, editHwnd, okHwnd, cancelHwnd} {
			w32.SendMessage(c, w32.WM_SETFONT, uintptr(font), 1)
		}
	}

	w32.ShowWindow(hwnd, w32.SW_SHOW)
	w32.UpdateWindow(hwnd)
	w32.SetForegroundWindow(hwnd)
	w32.SetFocus(editHwnd)

	// 消息循环；IsDialogMessage 让 Tab 切换焦点、回车触发"确定"、Esc 触发"取消"
	var msg w32.MSG
	for w32.GetMessage(&msg, 0, 0, 0) > 0 {
		if !w32.IsDialogMessage(hwnd, &msg) {
			w32.TranslateMessage(&msg)
			w32.DispatchMessage(&msg)
		}
	}

	return inputResult
}

// inputText 弹出输入框，返回用户输入（默认值预填）；取消返回 ""。
func inputText(title, prompt, def string) string {
	return nativeInputText(title, prompt, def)
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
	out, err := psRun(ps)
	if err != nil {
		log.Printf("文件选择面板执行失败: %v", err)
		dialog.Message("打开文件选择框失败：%v", err).Error()
		return nil
	}
	if out == "" {
		log.Println("文件选择面板未返回结果（可能用户取消）")
		return nil
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
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
	out, err := psRun(ps)
	if err != nil {
		log.Printf("目标设备选择框执行失败: %v", err)
		dialog.Message("打开目标设备选择框失败：%v", err).Error()
		return nil
	}
	var res []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
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
