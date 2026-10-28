package main

import (
	"sync"

	"github.com/getlantern/systray"
)

// 已配对设备记录：用于首次配对成功时发通知，重连不重复提示；
// 持久化到 config.json，进程重启后仍记住，不把重连误当"新配对"
var (
	pairedMu  sync.Mutex
	pairedSet = map[string]bool{}
)

func init() {
	pairedMu.Lock()
	pairedSet = getPairedMap()
	pairedMu.Unlock()
}

// isPaired 该设备名是否已受信任（持久化在 config.json 的 pairedDevices 中）
func isPaired(name string) bool {
	if name == "" {
		return false
	}
	pairedMu.Lock()
	defer pairedMu.Unlock()
	return pairedSet[name]
}

// markPaired 把设备标记为受信任并持久化；已存在则直接返回
func markPaired(name string) {
	if name == "" {
		return
	}
	pairedMu.Lock()
	already := pairedSet[name]
	pairedSet[name] = true
	pairedMu.Unlock()
	if !already {
		addPaired(name)
	}
}

// 设备列表/发送目标子菜单槽位
var (
	devSlots  []*deviceSlot // 设备列表子菜单槽位
	sendSlots []*deviceSlot // 发送文件目标子菜单槽位
)

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

// updateConfirmMenu 同步菜单显示的"接入需确认"状态
func updateConfirmMenu(m *systray.MenuItem) {
	if getRequireConfirm() {
		m.SetTitle("接入需确认: 开")
		m.Check()
	} else {
		m.SetTitle("接入需确认: 关")
		m.Uncheck()
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
