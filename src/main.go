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
	log.Printf("配置目录: %s, 密钥: %s, 系统: %s/%s, 主机名: %s", configDir, getToken(), runtime.GOOS, runtime.GOARCH, name)
	if getToken() == defaultToken {
		log.Println("警告: 正在使用内置默认配对密钥；同网段任何 ClipSync 只要密钥相同就能配对并收发剪贴板。" +
			"建议点托盘「配对密钥…」改成自己的密钥，两台设备填同一个值。")
	}
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
				if err := clip.SetText(string(body)); err != nil {
					log.Printf("写文字失败: %v", err)
				}
			case "image/png":
				if err := clip.SetImageSafe(body); err != nil {
					log.Printf("写图片失败: %v", err)
				}
			}
		case PacketPing:
			// 心跳：回 pong 让对端知道本机还活着
			netw.SendTo(ip, PacketHeader{Type: PacketPong}, nil)
		case PacketPong:
			// 收到 pong 本身已刷新 readLoop 里的 lastRecv，无需额外处理
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
			// 首次配对发系统通知；重连同一设备只轻量提示
			if firstPair(peer) {
				log.Printf("配对新设备: %s", peer)
				notify("ClipSync", fmt.Sprintf("已配对新设备：%s", peer))
			} else {
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
			cur := getToken()
			v := inputText("ClipSync 配对密钥",
				"当前配对密钥（可改成自己的）。两台设备填写相同的密钥即可自动配对，"+
					"不同则无法连接。4~64 个字符，不含空格与引号。", cur)
			v = strings.TrimSpace(v)
			if v == "" || v == cur {
				continue // 取消，或没有改动
			}
			nv, err := normalizeToken(v)
			if err != nil {
				dialog.Message("%s", err.Error()).Error()
				continue
			}
			if err := saveToken(nv); err != nil {
				log.Printf("保存配对密钥失败: %v", err)
				dialog.Message("保存失败：%v", err).Error()
				continue
			}
			log.Printf("配对密钥已更新（%d 个字符，%s）", len([]rune(nv)),
				map[bool]string{true: "仍是内置默认值", false: "已自定义"}[nv == defaultToken])
			// 用新密钥重建连接：对端若仍是旧密钥会认证失败，改完自动恢复
			netw.RefreshConnections()
			msg := "配对密钥已保存：" + nv + "\n\n已用新密钥重新连接。"
			if nv == defaultToken {
				msg += "\n\n⚠️ 这是内置默认密钥，同网段装了 ClipSync 的设备都能连上，建议改成自己的。"
			} else {
				msg += "\n另一台设备也要改成同一个密钥才能配对。"
			}
			dialog.Message("%s", msg).Info()
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
