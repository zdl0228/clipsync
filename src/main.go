package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/getlantern/systray"
	"github.com/sqweek/dialog"
)

//go:embed icon.png
var iconBytes []byte

//go:embed icon.ico
var iconIcoBytes []byte // Windows 托盘需要 ICO 格式

// 核心对象：网络层与剪贴板实例（由 onReady 初始化）
var (
	netw *Network
	clip *Clipboard
)

func main() {
	// 右键菜单发送模式：把文件/文件夹路径转发给已运行的托盘主进程，随后立即退出（不启动托盘）
	if len(os.Args) > 1 && os.Args[1] == "--send" {
		runSendHelper(os.Args[2:])
		return
	}
	initLog()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v", r)
		}
	}()
	log.Println("=== ClipSync 启动 ===")
	systray.Run(onReady, onExit)
}

// initLog 将日志输出重定向到配置目录（clipsync）下的 ClipSync.log；不可写时回退到主目录
func initLog() {
	dir := configDir
	path := filepath.Join(dir, "ClipSync.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// 配置目录不可写时回退到用户主目录
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			path = filepath.Join(home, "ClipSync.log")
			f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		}
		if err != nil {
			log.Printf("日志文件打开失败: %v", err)
			return
		}
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("=== ClipSync 日志已启用: %s ===", path)
}

func onReady() {
	name, _ := os.Hostname()
	log.Printf("配置目录: %s, 密钥: %s, 系统: %s/%s, 主机名: %s", configDir, token, runtime.GOOS, runtime.GOARCH, name)
	netw = NewNetwork(name)
	clip = NewClipboard()

	netw.OnPacket(func(ip string, h *PacketHeader, body []byte) {
		log.Printf("收到包: from=%s type=%s mime=%s size=%d", ip, h.Type, h.MimeType, len(body))
		switch h.Type {
		case PacketFileMeta:
			handleFileMeta(ip, h)
		case PacketFileData:
			handleFileData(ip, h, body)
		case PacketClipboard:
			clip.NoteIncoming(h.MimeType, body)
			switch h.MimeType {
			case "text/plain":
				clip.SetText(string(body))
			case "image/png":
				if err := clip.SetImageSafe(body); err != nil {
					log.Printf("写图片失败: %v", err)
				}
			}
		}
	})

	clip.OnChange(func() {
		mime, body := clip.ReadCurrent()
		log.Printf("本地剪贴板: mime=%s, size=%d", mime, len(body))
		if body == nil {
			return
		}
		netw.Send(PacketHeader{Type: PacketClipboard, MimeType: mime}, body)
	})

	netw.Start()
	go clip.Watch()
	go ipcListen() // 本地 IPC：接收右键菜单转发的文件路径

	if runtime.GOOS == "windows" {
		systray.SetIcon(iconIcoBytes)
	} else {
		systray.SetIcon(iconBytes)
	}
	systray.SetTooltip(fmt.Sprintf("ClipSync - %s", name))

	mOnline := systray.AddMenuItem("等待设备…", "")
	mOnline.Disable()
	systray.AddSeparator()

	mSync := systray.AddMenuItem("立即同步", "")

	// 发送文件到 ▸ 目标设备子菜单
	sendMenu := systray.AddMenuItem("发送文件到 ▸", "")
	sendSlots = make([]*deviceSlot, maxSlots)
	for i := 0; i < maxSlots; i++ {
		s := &deviceSlot{item: sendMenu.AddSubMenuItem("", "")}
		s.item.Hide()
		sendSlots[i] = s
	}
	// 传输进度行：空闲隐藏，发送/接收时显示
	progressItem = systray.AddMenuItem("就绪", "")
	progressItem.Hide()
	systray.AddSeparator()

	// 设备 ▸ 列表子菜单（点击断开）
	devMenu := systray.AddMenuItem("设备 ▸", "")
	devSlots = make([]*deviceSlot, maxSlots)
	for i := 0; i < maxSlots; i++ {
		s := &deviceSlot{item: devMenu.AddSubMenuItem("", "")}
		s.item.Hide()
		devSlots[i] = s
	}
	systray.AddSeparator()

	mToken := systray.AddMenuItem("配对密钥…", "")
	mDirect := systray.AddMenuItem("直连指定 IP…", "")
	mBroadcast := systray.AddMenuItem("自动发现广播", "")
	mBroadcast.Check()
	systray.AddSeparator()

	// 右键发送（仅 Windows）：默认开启；开启时幂等刷新注册表（自动修正 exe 路径），关闭时移除
	var mCtx *systray.MenuItem
	if runtime.GOOS == "windows" {
		if getContextMenuPref() != "off" {
			if err := enableContextMenu(); err != nil {
				log.Printf("注册右键发送菜单失败: %v", err)
			} else {
				setContextMenuPref("on")
				log.Println("已注册右键发送菜单")
			}
			mCtx = systray.AddMenuItem("右键发送: 开", "")
			mCtx.Check()
		} else {
			disableContextMenu()
			mCtx = systray.AddMenuItem("右键发送: 关", "")
			mCtx.Uncheck()
		}
		systray.AddSeparator()
	}
	mAutoStart := systray.AddMenuItem("开机启动: 关", "")
	mQuit := systray.AddMenuItem("退出", "")

	// 发送子菜单：点击目标设备 → 多选文件/文件夹 → 逐个发送到该设备
	for _, s := range sendSlots {
		go func(s *deviceSlot) {
			for range s.item.ClickedCh {
				ip := s.getIP()
				if ip == "" {
					continue
				}
				paths := selectPaths()
				if len(paths) == 0 {
					continue
				}
				log.Printf("选择发送 %d 个文件/文件夹 → %s", len(paths), ip)
				// 多选（>1 项）时抑制单条通知，全部完成后统一汇总一条
				batch := len(paths) > 1
				for _, p := range paths {
					if st, err := os.Stat(p); err == nil && st.IsDir() {
						sendFolderTo(ip, p, batch)
					} else {
						sendFileTo(ip, p, batch)
					}
				}
				if batch {
					notify("ClipSync", fmt.Sprintf("已发送 %d 个文件/文件夹", len(paths)))
				}
			}
		}(s)
	}
	// 设备子菜单：点击设备 → 断开该设备
	for _, s := range devSlots {
		go func(s *deviceSlot) {
			for range s.item.ClickedCh {
				ip := s.getIP()
				if ip == "" {
					continue
				}
				netw.Disconnect(ip)
			}
		}(s)
	}

	netw.OnPeerChange(func(ip string, peer string, online bool) {
		if online {
			mOnline.SetTitle("已连接: " + peer)
			// 首次配对成功发系统通知；重连同一设备不重复提示
			pairedMu.Lock()
			first := !pairedSet[peer]
			if first {
				pairedSet[peer] = true
			}
			pairedMu.Unlock()
			if first {
				addPaired(peer) // 持久化，重启后不再当作新配对重复提示
				log.Printf("配对新设备: %s", peer)
				notify("ClipSync", fmt.Sprintf("已配对新设备：%s", peer))
			} else {
				// 已配对设备重连：轻量提醒"已连接"，不再重复"新配对"
				log.Printf("已连接设备: %s", peer)
				notify("ClipSync", fmt.Sprintf("已连接设备：%s", peer))
			}
		} else {
			// 对端断开：清理该 IP 未完成的接收状态与进度计数，避免泄漏
			clearRecv(ip)
			mOnline.SetTitle("等待设备…")
		}
		updateDeviceMenus()
	})
	updateDeviceMenus()

	go func() {
		for range mSync.ClickedCh {
			mime, body := clip.ReadCurrent()
			if body != nil {
				log.Printf("手动立即同步: mime=%s size=%d", mime, len(body))
				netw.Send(PacketHeader{Type: PacketClipboard, MimeType: mime}, body)
			}
		}
	}()

	go func() {
		for range mDirect.ClickedCh {
			ip := strings.TrimSpace(inputIP())
			if ip == "" {
				continue
			}
			log.Printf("手动直连: %s", ip)
			netw.Dial(ip)
			notify("ClipSync", fmt.Sprintf("正在直连：%s", ip))
		}
	}()

	go func() {
		for range mBroadcast.ClickedCh {
			on := !netw.BroadcastEnabled()
			netw.SetBroadcast(on)
			log.Printf("自动发现广播: %s", map[bool]string{true: "开", false: "关"}[on])
			if on {
				mBroadcast.Check()
			} else {
				mBroadcast.Uncheck()
			}
		}
	}()

	// 默认开机启动：除非用户此前明确关闭过，否则启动时自动开启
	if getAutoStartPref() != "off" && !isAutoStartEnabled() {
		if err := setAutoStart(true); err != nil {
			log.Printf("自动开启开机启动失败: %v", err)
		} else {
			setAutoStartPref("on")
			log.Println("已自动开启开机自启")
		}
	}
	updateAutoStartMenu(mAutoStart)

	// 拖文件到菜单栏图标 → 发送给所有已认证在线设备
	systray.SetOnDrop(onDropFiles)

	go func() {
		for range mAutoStart.ClickedCh {
			next := !isAutoStartEnabled()
			if err := setAutoStart(next); err != nil {
				dialog.Message("设置开机启动失败：%v", err).Error()
				continue
			}
			v := "on"
			if !next {
				v = "off"
			}
			setAutoStartPref(v)
			log.Printf("设置开机自启: %s", map[bool]string{true: "开", false: "关"}[next])
			updateAutoStartMenu(mAutoStart)
		}
	}()

	// 右键发送开关：点击在"注册/移除"右键菜单之间切换，并记录偏好
	if mCtx != nil {
		go func() {
			for range mCtx.ClickedCh {
				on := !isContextMenuEnabled()
				if on {
					if err := enableContextMenu(); err != nil {
						dialog.Message("注册右键发送菜单失败：%v", err).Error()
						continue
					}
					setContextMenuPref("on")
					mCtx.SetTitle("右键发送: 开")
					mCtx.Check()
				} else {
					if err := disableContextMenu(); err != nil {
						dialog.Message("移除右键发送菜单失败：%v", err).Error()
						continue
					}
					setContextMenuPref("off")
					mCtx.SetTitle("右键发送: 关")
					mCtx.Uncheck()
				}
				log.Printf("右键发送菜单: %s", map[bool]string{true: "开", false: "关"}[on])
			}
		}()
	}

	go func() {
		for range mToken.ClickedCh {
			dialog.Message("本机配对密钥：%s\n\n请让两台设备的 config.json 中 token 保持此值一致，两端才能加密连接。", token).Info()
		}
	}()

	go func() {
		<-mQuit.ClickedCh
		systray.Quit()
	}()

	log.Printf("ClipSync 已启动 (Go, %s)", runtime.GOOS)
}

func onExit() {
}
