package main

import (
	_ "embed"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	// 版本查询必须排在 initLog 之前：结果要打到标准输出，不能进日志文件。
	// 注意 Windows 的 exe 是 windowsgui 子系统（无控制台），stdout 看不到，
	// 那边靠托盘菜单的版本项或日志首行来确认版本。
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(versionDetail())
		return
	}
	initLog()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v", r)
		}
	}()
	// 无界面改密钥：弹窗不可用（远程桌面、精简系统、被安全软件限制）时的兜底，
	// 也方便脚本化批量部署。用法：ClipSync.exe --set-token 我的密钥
	if len(os.Args) > 2 && os.Args[1] == "--set-token" {
		runSetToken(os.Args[2])
		return
	}
	log.Println("=== ClipSync 启动 ===", versionDetail())
	systray.Run(onReady, onExit)
}

// initLog 将日志输出重定向到配置目录（clipsync）下的 ClipSync.log；不可写时回退到主目录。
// 用 0600：日志里会出现文件名、设备名、IP 等个人信息，只有本人需要读。
func initLog() {
	dir := configDir
	path := filepath.Join(dir, "ClipSync.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		// 配置目录不可写时回退到用户主目录
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			path = filepath.Join(home, "ClipSync.log")
			f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		}
		if err != nil {
			log.Printf("日志文件打开失败: %v", err)
			return
		}
	}
	log.SetOutput(f)
	// 已存在的日志文件不会因 O_CREATE 的 mode 改变权限，必须显式 chmod 一次
	_ = f.Chmod(0600)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("=== ClipSync 日志已启用: %s ===", path)
}

func onReady() {
	name, _ := os.Hostname()
	// 密钥不写明文进日志：日志文件经常被贴进聊天窗口/工单，而密钥就是
	// 同网段收发剪贴板的唯一凭证，泄露等于把剪贴板交出去
	tokenDesc := "内置默认值"
	if getToken() != defaultToken {
		tokenDesc = "已自定义"
	}
	log.Printf("配置目录: %s, 密钥: %d 个字符（%s）, 系统: %s/%s, 主机名: %s",
		configDir, len([]rune(getToken())), tokenDesc, runtime.GOOS, runtime.GOARCH, name)
	if getToken() == defaultToken {
		log.Println("警告: 正在使用内置默认配对密钥；同网段任何 ClipSync 只要密钥相同就能配对并收发剪贴板。" +
			"建议点托盘「配对密钥…」改成自己的密钥，两台设备填同一个值。")
	}
	netw = NewNetwork(name)
	clip = NewClipboard()

	// 通知授权必须在启动时就请求：等到第一次发通知再请求，异步授权来不及，
	// 那条通知会直接丢掉，之后也再没有机会弹授权框
	notifyPrepare()
	if st := notifyPermission(); st != "" {
		log.Printf("通知权限: %s", st)
	}

	netw.OnPacket(func(ip string, h *PacketHeader, body []byte) {
		log.Printf("收到包: from=%s type=%s mime=%s size=%d", ip, h.Type, h.MimeType, len(body))
		switch h.Type {
		case PacketFileMeta:
			handleFileMeta(ip, h)
		case PacketFileData:
			handleFileData(ip, h, body)
		case PacketFileDir:
			// 目录结构/空目录：只建目录，不产生文件
			handleFileDir(ip, h)
		case PacketClipboard:
			// 超大内容直接拒绝：写入剪贴板会卡住整个 UI，而且很可能是
			// 对端误发（比如复制了一个巨大的图片）而不是真需要同步的东西
			if len(body) > maxClipboardBytes {
				log.Printf("忽略过大的剪贴板内容: %d 字节（上限 %d）", len(body), maxClipboardBytes)
				return
			}
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
		// 与接收侧同一条上限：几十 MB 的图片/文档整包打包会把内存顶上去，
		// 而且这种内容本来也不适合走剪贴板同步
		if len(body) > maxClipboardBytes {
			log.Printf("剪贴板内容过大，跳过同步: %d 字节（上限 %d）", len(body), maxClipboardBytes)
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
	systray.SetTooltip(fmt.Sprintf("ClipSync %s - %s", versionLabel(), name))

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

	mToken := systray.AddMenuItem("配对密钥…", "")
	mDirect := systray.AddMenuItem("直连指定 IP…", "")
	mBroadcast := systray.AddMenuItem("自动发现广播", "")
	mBroadcast.Check()
	systray.AddSeparator()

	// 右键发送：Windows 写注册表、macOS 装 Finder 服务（见 contextmenu_*.go）。
	// 默认开启；开启时幂等刷新（自动修正 exe 路径），关闭时移除
	var mCtx *systray.MenuItem
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if getContextMenuPref() != "off" {
			if err := enableContextMenu(); err != nil {
				log.Printf("注册右键发送菜单失败: %v", err)
			} else {
				setContextMenuPref("on")
				log.Printf("已注册右键发送菜单（%s）", ctxMenuLabel)
			}
			mCtx = systray.AddMenuItem(ctxMenuLabel+": 开", "")
			mCtx.Check()
		} else {
			disableContextMenu()
			mCtx = systray.AddMenuItem(ctxMenuLabel+": 关", "")
			mCtx.Uncheck()
		}
		systray.AddSeparator()
	}
	mAutoStart := systray.AddMenuItem("开机启动: 关", "")
	systray.AddSeparator()
	// 版本号常驻菜单底部（禁用态，点击无反应）：两台机器版本不一致时，
	// 一眼就能看出是哪台没更新，比翻日志或看文件时间靠谱
	mVersion := systray.AddMenuItem("版本 "+versionLabel(), "")
	mVersion.Disable()
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
				ok, failed := 0, 0
				for _, p := range paths {
					if st, err := os.Stat(p); err == nil && st.IsDir() {
						fo, ff := sendFolderTo(ip, p, batch)
						ok += fo
						failed += ff
					} else if sendFileTo(ip, p, batch) {
						ok++
					} else {
						failed++
					}
				}
				if batch {
					notifyBatchResult("已发送", ok, failed)
				}
			}
		}(s)
	}
	// 设备子菜单已删：原来点击是"断开该设备"，但 UDP 自动发现几秒后就会
	// 重新拨号连回去，断开毫无意义；设备状态看顶部标题即可。

	netw.OnPeerChange(func(ip string, peer string, online bool) {
		if online {
			mOnline.SetTitle("已连接: " + peer)
			// 首次配对发系统通知；重连同一设备只轻量提示。
			// 连接被替换/网络抖动会在几秒内连着触发两次上线，通知去抖挡掉重复的。
			key := peer + "@" + ip
			switch {
			case !shouldNotifyOnline(key):
				log.Printf("已连接设备（抑制重复通知）: %s", peer)
			case firstPair(peer, ip):
				log.Printf("配对新设备: %s", peer)
				notify("ClipSync", fmt.Sprintf("已配对新设备：%s", peer))
			default:
				log.Printf("已连接设备: %s", peer)
				notify("ClipSync", fmt.Sprintf("已连接设备：%s", peer))
			}
		} else {
			// 对端断开：清理该 IP 未完成的接收状态与进度计数，避免泄漏
			clearRecv(ip)
			// 多设备时不能因为一台掉线就把标题改回"等待设备…"——
			// 另一台其实还连着，标题却显示没设备，会让人以为同步断了
			if !netw.PeerOnline() {
				mOnline.SetTitle("等待设备…")
			}
		}
		updateDeviceMenus()
	})
	updateDeviceMenus()

	go func() {
		for range mSync.ClickedCh {
			mime, body := clip.ReadCurrent()
			if body == nil {
				// 剪贴板里是文件、HTML 之类不支持的格式时也要说一句，
				// 否则点「立即同步」看起来和没点一样
				notify("ClipSync", "剪贴板里没有可同步的文本或图片")
				continue
			}
			if !netw.PeerOnline() {
				notify("ClipSync", "没有设备在线，无法同步")
				continue
			}
			log.Printf("手动立即同步: mime=%s size=%d", mime, len(body))
			netw.Send(PacketHeader{Type: PacketClipboard, MimeType: mime}, body)
			notify("ClipSync", "已同步剪贴板内容")
		}
	}()

	go func() {
		for range mDirect.ClickedCh {
			ip := strings.TrimSpace(inputIP())
			if ip == "" {
				continue
			}
			if net.ParseIP(ip) == nil {
				dialog.Message("IP 地址格式不对：%s", ip).Error()
				continue
			}
			log.Printf("手动直连: %s", ip)
			netw.Dial(ip)
			notify("ClipSync", fmt.Sprintf("正在直连：%s", ip))
			// 拨号是异步的，3 秒后回报结果。原来只发"正在直连"就没了下文，
			// 连不上时用户完全不知道是 IP 写错、对端没开还是密钥不一致。
			go func(ip string) {
				time.Sleep(3 * time.Second)
				if netw.IsPeerAuthed(ip) {
					notify("ClipSync", "已连上："+ip)
					return
				}
				notify("ClipSync", "直连失败："+ip+"\n请确认对端已运行、IP 正确、两端密钥一致")
			}(ip)
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

	// 默认开机启动：除非用户此前明确关闭过，否则首次启动时自动开启。
	// 这里用 launchAgentExists 而非 isAutoStartEnabled：后者在用户从「系统设置」
	// 取消勾选后也会返回 false，若据此自动重开，会把用户主动关闭的行为又强行打开。
	if getAutoStartPref() != "off" && !launchAgentExists() {
		if err := setAutoStart(true); err != nil {
			log.Printf("自动开启开机启动失败: %v", err)
		} else {
			setAutoStartPref("on")
			log.Println("已自动开启开机自启")
		}
	}
	updateAutoStartMenu(mAutoStart)

	// 通知是发送/接收的唯一可见反馈，而权限被拒时系统会静默丢弃通知、
	// 用户只会觉得"点了没反应"。启动时自检一次：异常就弹窗给出修复路径，
	// 同一天最多提示一次（也可能是用户有意关掉的，不该每次开机都打扰）。
	go func() {
		st := notifyPermission()
		switch st {
		case "", "已允许", "临时允许":
			return
		}
		log.Printf("通知权限异常: %s", st)
		today := time.Now().Format("2006-01-02")
		if getNotifyWarned() == today {
			return
		}
		setNotifyWarned(today)
		dialog.Message("系统通知权限当前是「%s」，ClipSync 的发送、接收提示都会被系统静默丢弃。\n\n"+
			"修复：系统设置 → 通知 → ClipSync → 打开「允许通知」，样式选「横幅」或「提醒」。", st).Info()
	}()

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

	// 右键发送开关：点击在"注册/移除"之间切换，并记录偏好
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
					mCtx.SetTitle(ctxMenuLabel + ": 开")
					mCtx.Check()
					// mac 的服务藏在右键子菜单里，开启后说明一下从哪找
					if hint := contextMenuHint(); hint != "" {
						dialog.Message("%s", hint).Info()
					}
				} else {
					if err := disableContextMenu(); err != nil {
						dialog.Message("移除右键发送菜单失败：%v", err).Error()
						continue
					}
					setContextMenuPref("off")
					mCtx.SetTitle(ctxMenuLabel + ": 关")
					mCtx.Uncheck()
				}
				log.Printf("右键发送菜单: %s", map[bool]string{true: "开", false: "关"}[on])
			}
		}()
	}

	go func() {
		for range mToken.ClickedCh {
			cur := getToken()
			// 先记一行日志：万一输入框没弹出来，靠这行就能区分"菜单没触发"
			// 和"弹窗环节失败"，不用再猜
			log.Printf("打开配对密钥输入框（当前 %d 个字符）", len([]rune(cur)))
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

// runSetToken 处理 `--set-token <密钥>`：校验后直接写盘并提示结果。
// 写的是 config.json，正在运行的托盘进程内存里仍是旧值，必须重启才生效，
// 所以提示里要说清楚，否则用户会以为没成功（连接依旧认证失败）。
func runSetToken(raw string) {
	nv, err := normalizeToken(raw)
	if err != nil {
		log.Printf("--set-token 未通过校验: %v", err)
		dialog.Message("密钥无效：%v", err).Error()
		return
	}
	if err := saveToken(nv); err != nil {
		log.Printf("--set-token 保存失败: %v", err)
		dialog.Message("保存失败：%v", err).Error()
		return
	}
	log.Printf("--set-token 已保存（%d 个字符）", len([]rune(nv)))
	dialog.Message("配对密钥已保存（%d 个字符）。\n\n"+
		"托盘里的 ClipSync 如果正在运行，需要先「退出」再重新打开，新密钥才生效。"+
		"\n另一台设备记得填同一个密钥。", len([]rune(nv))).Info()
}

func onExit() {
}
