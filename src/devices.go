package main

import (
	"sync"
	"time"

	"github.com/getlantern/systray"
)

// 已配对设备记录：用于首次配对成功时发通知，重连不重复提示；
// 持久化到 config.json，进程重启后仍记住，不把重连误当"新配对"
var (
	pairedMu  sync.Mutex
	pairedSet = map[string]bool{}
	// pairedLoaded 懒加载标记。原来靠包级 init() 读配置，而 init 的先后
	// 取决于文件名字典序（config.go 恰好在 devices.go 前面才没出事）——
	// 一旦有人改名，这里就会在 configDir 还没设好时去读 config.json。
	pairedLoaded bool
)

// loadPairedOnce 首次配对时才读一次磁盘记录（此时 initConfig 一定已执行完）
func loadPairedOnce() {
	if pairedLoaded {
		return
	}
	pairedSet = getPairedMap()
	pairedLoaded = true
}

// firstPair 记下"该设备首次配对"（用于发通知，重连不重复提示），
// 返回本次是否为首次；首次会持久化到 config.json，重启后不再误报"新配对"。
// 记录键是"设备名@IP"：只按设备名去重的话，办公室里两台同名 Windows
// （一堆机器都叫 DESKTOP-XXXX）第二台永远不提示配对；反过来只看 IP 又会
// 在 DHCP 换地址后重复提示。两者组合最贴近"这是不是一台没见过的机器"。
func firstPair(name, ip string) bool {
	if name == "" {
		return false
	}
	key := name + "@" + ip
	pairedMu.Lock()
	loadPairedOnce()
	first := !pairedSet[key]
	pairedSet[key] = true
	pairedMu.Unlock()
	if first {
		addPaired(key)
	}
	return first
}

// 设备列表/发送目标子菜单槽位
var (
	devSlots  []*deviceSlot // 设备列表子菜单槽位
	sendSlots []*deviceSlot // 发送文件目标子菜单槽位
)

// "已连接"通知去抖：两端同时发现彼此时会互相拨号，连接被替换一次就会触发
// 两次 onPeerChange(online=true)，用户连着收到两条一模一样的横幅。
// 同一条设备在 5s 内只提示一次；菜单标题不受影响，照常更新。
var (
	onlineNotifyMu sync.Mutex
	onlineNotifyAt = map[string]time.Time{}
)

// shouldNotifyOnline 判断本次上线是否值得发通知
func shouldNotifyOnline(key string) bool {
	onlineNotifyMu.Lock()
	defer onlineNotifyMu.Unlock()
	if t, ok := onlineNotifyAt[key]; ok && time.Since(t) < 5*time.Second {
		return false
	}
	onlineNotifyAt[key] = time.Now()
	return true
}

// maxSlots 托盘设备子菜单最大槽位数
const maxSlots = 10

// deviceSlot 子菜单中的一个可复用槽位（点击对应某台设备）
type deviceSlot struct {
	item *systray.MenuItem
	mu   sync.Mutex
	ip   string
}

// setIP 线程安全写入槽位对应设备 IP
func (s *deviceSlot) setIP(ip string) {
	s.mu.Lock()
	s.ip = ip
	s.mu.Unlock()
}

// getIP 线程安全读取槽位对应设备 IP
func (s *deviceSlot) getIP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ip
}

// updateAutoStartMenu 同步菜单显示的开机启动状态
func updateAutoStartMenu(m *systray.MenuItem) {
	if isAutoStartEnabled() {
		m.SetTitle("开机启动: 开")
	} else {
		m.SetTitle("开机启动: 关")
	}
}

// updateDeviceMenus 刷新设备列表子菜单与发送文件目标子菜单
func updateDeviceMenus() {
	peers := netw.Peers()

	// 设备列表子菜单（含认证中）
	for i, s := range devSlots {
		if i < len(peers) {
			p := peers[i]
			s.setIP(p.IP)
			t := p.Name
			if t == "" {
				t = p.IP
			}
			if !p.Authed {
				t += " (认证中)"
			}
			s.item.SetTitle(t)
			s.item.Show()
		} else {
			s.setIP("")
			s.item.Hide()
		}
	}

	// 发送文件目标子菜单（仅已认证可发送）
	idx := 0
	for _, p := range peers {
		if !p.Authed {
			continue
		}
		if idx >= len(sendSlots) {
			break
		}
		s := sendSlots[idx]
		s.setIP(p.IP) // 与 getIP 同锁，避免与托盘点击回调竞争
		t := p.Name
		if t == "" {
			t = p.IP
		}
		s.item.SetTitle(t)
		s.item.Show()
		idx++
	}
	for i := idx; i < len(sendSlots); i++ {
		sendSlots[i].setIP("")
		sendSlots[i].item.Hide()
	}
}
