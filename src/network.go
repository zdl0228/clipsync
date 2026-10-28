package main

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 心跳参数：20s 发一次 ping，90s 收不到任何包判定掉线（容忍短暂卡顿/休眠唤醒）
const (
	heartbeatInterval = 20 * time.Second
	peerTimeout       = 90 * time.Second
	tcpKeepAlive      = 15 * time.Second
)

type peer struct {
	ip       string
	name     string
	conn     net.Conn
	authed   bool
	lastDial time.Time
	lastRecv time.Time // 最近一次收到该对端任意包的时间（心跳超时判定）
	// pongSeen 该对端至少回应过一次心跳。旧版本没有 ping/pong，若对它做超时
	// 剔除会造成"每 90s 断一次再重连"的抖动，所以只对确认支持心跳的对端生效。
	pongSeen bool
}

// PeerInfo 对外暴露的对端信息
type PeerInfo struct {
	IP     string
	Name   string
	Authed bool
}

type Network struct {
	deviceName string

	mu    sync.RWMutex
	peers map[string]*peer

	broadcastOn atomic.Bool
	// 收到新设备 hello 时向广播协程发送信号，使其立即临时广播一次，让新设备能发现本机
	helloTrigger chan struct{}

	onPeerChange func(ip string, name string, online bool)
	onPacket     func(ip string, h *PacketHeader, body []byte)
}

func NewNetwork(name string) *Network {
	n := &Network{
		deviceName:   name,
		peers:        make(map[string]*peer),
		helloTrigger: make(chan struct{}, 1),
	}
	n.broadcastOn.Store(true)
	return n
}

func (n *Network) OnPeerChange(f func(string, string, bool))      { n.onPeerChange = f }
func (n *Network) OnPacket(f func(string, *PacketHeader, []byte)) { n.onPacket = f }

func tlsConfig() *tls.Config {
	cert, err := loadCert()
	if err != nil {
		log.Printf("加载证书失败: %v", err)
		return nil
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // 自签名证书；身份由应用层 token 认证保证
		MinVersion:         tls.VersionTLS12,
	}
}

func (n *Network) Start() {
	go n.udpBroadcast()
	go n.udpListen()
	go n.tcpListen()
	go n.heartbeat()
}

// heartbeat 应用层心跳：
// TCP keepalive 在多数系统上要十几分钟才判定断链，Wi-Fi 换网 / 对端异常重启
// （无 FIN/RST）时本机会长期显示"已连接"却发不出东西。这里主动每 20s 发 ping，
// 超过 90s 没收到该对端任何包即视为掉线并断开，让 UDP 发现重新拉起来。
func (n *Network) heartbeat() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		var stale []string
		var alive []string

		n.mu.RLock()
		for ip, p := range n.peers {
			if p.conn == nil || !p.authed {
				continue
			}
			if p.pongSeen && !p.lastRecv.IsZero() && now.Sub(p.lastRecv) > peerTimeout {
				stale = append(stale, ip)
			} else {
				alive = append(alive, ip)
			}
		}
		n.mu.RUnlock()

		for _, ip := range stale {
			log.Printf("心跳超时，断开对端: %s（%s 无响应）", ip, peerTimeout)
			n.mu.RLock()
			p := n.peers[ip]
			n.mu.RUnlock()
			if p != nil {
				n.dropPeer(ip, p.conn)
			}
		}
		for _, ip := range alive {
			n.SendTo(ip, PacketHeader{Type: PacketPing}, nil)
		}
	}
}

func (n *Network) helloPacket() []byte {
	return PackPacket(PacketHeader{Type: PacketHello, DeviceName: n.deviceName}, nil)
}

func (n *Network) authPacket() []byte {
	return PackPacket(PacketHeader{Type: PacketAuth, DeviceName: n.deviceName, Token: getToken()}, nil)
}

// ---------- UDP 发现 ----------
// udpBroadcast 设备发现广播：
//   - 存在未认证/未连接设备时，按动态间隔 + 随机退避发送 hello 快速发现
//   - 所有对端均已认证在线时静默（不发广播，对内网零影响），仅保留监听；
//     收到新设备 hello 时（helloTrigger）立即临时广播一次，让新设备能发现本机并自动配对
//   - 可被关闭（手动直连模式）
func (n *Network) udpBroadcast() {
	addr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", UDPPort))
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Println("UDP broadcast:", err)
		return
	}
	defer conn.Close()
	pkt := n.helloPacket()
	ticker := time.NewTicker(n.broadcastInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-n.helloTrigger: // 发现新设备，立即评估并广播一次
		}
		ticker.Reset(n.broadcastInterval())
		if n.broadcastOn.Load() && !n.allAuthedOnline() {
			conn.Write(pkt)
			// 随机退避：避免多设备广播包同时发出造成 Wi-Fi 冲突重传
			time.Sleep(time.Duration(rand.Intn(500)) * time.Millisecond)
		}
	}
}

// isLocalIP 判断 ip 是否属于本机任一网卡地址（用于跳过自己发起的 UDP 广播包）
func isLocalIP(ip string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.String() == ip {
			return true
		}
	}
	return false
}

// allAuthedOnline 是否所有已知对端都已认证且在线；是则停止主动广播（静默，仅监听）
func (n *Network) allAuthedOnline() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if len(n.peers) == 0 {
		return false // 无对端时仍需广播发现
	}
	for _, p := range n.peers {
		if !p.authed || p.conn == nil {
			return false
		}
	}
	return true
}

// broadcastInterval 动态广播间隔：无设备快速发现，有设备拉长降低开销
func (n *Network) broadcastInterval() time.Duration {
	n.mu.RLock()
	online, authed := 0, 0
	for _, p := range n.peers {
		if p.conn != nil {
			online++
		}
		if p.authed {
			authed++
		}
	}
	n.mu.RUnlock()
	if authed == 0 {
		return 5 * time.Second // 无已认证设备，快速发现
	}
	if online == authed {
		return 30 * time.Second // 全部已建立并认证，长间隔保活
	}
	return 15 * time.Second // 部分认证中，中等间隔
}

// SetBroadcast 开启/关闭 UDP 自动发现广播（手动直连时可关闭）
func (n *Network) SetBroadcast(on bool) { n.broadcastOn.Store(on) }

// BroadcastEnabled 当前是否允许 UDP 广播
func (n *Network) BroadcastEnabled() bool { return n.broadcastOn.Load() }

// Peers 返回当前已知对端列表（含连接/认证状态），按 IP 排序保证显示顺序稳定
func (n *Network) Peers() []PeerInfo {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]PeerInfo, 0, len(n.peers))
	for _, p := range n.peers {
		out = append(out, PeerInfo{IP: p.ip, Name: p.name, Authed: p.authed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// SendTo 仅向指定对端发送数据（文件发送等多设备场景按目标选择）
func (n *Network) SendTo(ip string, h PacketHeader, body []byte) {
	h.DeviceName = n.deviceName
	h.Token = getToken()
	h.BodyLen = len(body)
	pkt := PackPacket(h, body)

	n.mu.RLock()
	p := n.peers[ip]
	n.mu.RUnlock()
	if p == nil || !p.authed || p.conn == nil {
		return
	}
	// 写操作设超时，避免对端不读导致永久阻塞
	p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := p.conn.Write(pkt); err != nil {
		p.conn.SetWriteDeadline(time.Time{})
		n.dropPeer(p.ip, p.conn)
		return
	}
	p.conn.SetWriteDeadline(time.Time{})
}

// IsPeerAuthed 判断指定对端是否已认证（用于发送前/中断检查）
func (n *Network) IsPeerAuthed(ip string) bool {
	n.mu.RLock()
	p := n.peers[ip]
	n.mu.RUnlock()
	return p != nil && p.authed
}

// Dial 手动直连指定 IP（不依赖 UDP 发现）
func (n *Network) Dial(ip string) {
	go n.dialPeer(ip)
}

// Disconnect 手动断开指定对端
func (n *Network) Disconnect(ip string) {
	n.mu.RLock()
	p := n.peers[ip]
	n.mu.RUnlock()
	if p != nil {
		n.dropPeer(ip, p.conn)
	}
}

func (n *Network) udpListen() {
	addr, _ := net.ResolveUDPAddr("udp4", fmt.Sprintf(":%d", UDPPort))
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		log.Println("UDP listen:", err)
		return
	}
	defer conn.Close()
	buf := make([]byte, 65536)
	for {
		rlen, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		ip := remote.IP.String()
		// 逐包 recover：UDP 端口是暴露给整个局域网的，任何一个畸形包引发的
		// panic 都不该让发现循环整体退出（那等于永久失去自动发现能力）。
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("UDP 包处理 panic（已忽略）: %v", r)
				}
			}()
			n.handleHello(buf[:rlen], ip)
		}()
	}
}

// handleHello 处理一个 UDP hello 包：登记对端，需要时发起 TCP 拨号。
func (n *Network) handleHello(data []byte, ip string) {
	h, _, err := UnpackPacket(data)
	// hello 只用于发现，不含敏感 token，无需校验 token
	if err != nil || h.Type != PacketHello {
		return
	}
	// 收到本机自己发出的广播/回环包则跳过；用 IP 判断而非设备名比较，
	// 避免两台主机名相同的设备互相漏发现
	if isLocalIP(ip) {
		return
	}

	now := time.Now()
	n.mu.Lock()
	p, exists := n.peers[ip]
	needDial := false
	if !exists {
		n.peers[ip] = &peer{ip: ip, name: h.DeviceName, lastDial: now}
		needDial = true
	} else {
		if p.conn == nil || !p.authed {
			// 节流：避免认证完成前每 2 秒重复拨号
			if now.Sub(p.lastDial) > 5*time.Second {
				p.lastDial = now
				needDial = true
			}
		}
		if p.name == "" {
			p.name = h.DeviceName
		}
	}
	n.mu.Unlock()

	// 新设备或连接已断开时重连
	if needDial {
		log.Printf("发现/重连对端: %s @ %s", h.DeviceName, ip)
		// 通知广播协程立即临时广播一次，让新设备能发现本机
		select {
		case n.helloTrigger <- struct{}{}:
		default:
		}
		go n.dialPeer(ip)
	}
}

// ---------- TCP 连接 ----------

// dupConnWindow 判定"重复拨号"的时间窗：两端同时发现彼此时会各自拨号，
// 后到的那条会把先到的踢掉，表现就是连接抖动 + 重复的"已连接"通知。
const dupConnWindow = 10 * time.Second

// adoptConn 把新连接登记为某 IP 的当前连接，返回是否采纳。
// 若该 IP 已有一条"近期还在收包"的认证连接，说明这条是重复拨号产生的，
// 保留原连接、丢弃新的。旧连接若只是半死（对端进程已退出会有 FIN，走 dropPeer），
// 这里的时间窗很短，不会拖延重连。
func (n *Network) adoptConn(ip string, conn net.Conn) bool {
	n.mu.Lock()
	old := n.peers[ip]
	if old != nil && old.authed && old.conn != nil && old.conn != conn &&
		time.Since(old.lastRecv) < dupConnWindow {
		n.mu.Unlock()
		return false
	}
	name := ""
	if old != nil {
		name = old.name
		if old.conn != nil && old.conn != conn {
			old.conn.Close() // 关闭旧连接，避免泄漏
		}
	}
	n.peers[ip] = &peer{ip: ip, name: name, conn: conn}
	n.mu.Unlock()
	return true
}

func (n *Network) dialPeer(ip string) {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", TCPPort))
	d := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: tcpKeepAlive}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		return
	}
	if tc, ok := raw.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(tcpKeepAlive)
	}
	cfg := tlsConfig()
	if cfg == nil {
		raw.Close()
		return
	}
	conn := tls.Client(raw, cfg)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.Handshake(); err != nil {
		conn.Close()
		return
	}
	conn.SetDeadline(time.Time{})
	conn.Write(n.authPacket())

	if !n.adoptConn(ip, conn) {
		conn.Close()
		log.Printf("已有活跃连接，丢弃本次重复拨号: %s", ip)
		return
	}
	log.Println("已建立连接(待认证):", ip)
	go n.readLoop(ip, conn)
}

func (n *Network) tcpListen() {
	addr, _ := net.ResolveTCPAddr("tcp4", fmt.Sprintf(":%d", TCPPort))
	ln, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		log.Println("TCP listen:", err)
		return
	}
	for {
		raw, err := ln.AcceptTCP()
		if err != nil {
			continue
		}
		go func(raw *net.TCPConn) {
			raw.SetKeepAlive(true)
			raw.SetKeepAlivePeriod(tcpKeepAlive)
			cfg := tlsConfig()
			if cfg == nil {
				raw.Close()
				return
			}
			conn := tls.Server(raw, cfg)
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if err := conn.Handshake(); err != nil {
				conn.Close()
				return
			}
			conn.SetDeadline(time.Time{})
			conn.Write(n.authPacket())
			ip := raw.RemoteAddr().(*net.TCPAddr).IP.String()
			if !n.adoptConn(ip, conn) {
				conn.Close()
				log.Printf("已有活跃连接，丢弃重复接入: %s", ip)
				return
			}
			go n.readLoop(ip, conn)
		}(raw)
	}
}

// readLoop 精确读包；首个包必须是 auth 认证，超限直接断开防错位
func (n *Network) readLoop(ip string, conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("readLoop panic: %v", r)
		}
	}()
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, 1024*1024)
	authed := false
	for {
		// 未认证连接设读超时：防对端不认证而无限占资源
		if !authed {
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		} else {
			conn.SetReadDeadline(time.Time{})
		}
		hl := make([]byte, 4)
		if _, err := io.ReadFull(reader, hl); err != nil {
			break
		}
		hlen := binary.BigEndian.Uint32(hl)
		if hlen == 0 || hlen > maxHeaderBytes {
			break
		}
		hbuf := make([]byte, hlen)
		if _, err := io.ReadFull(reader, hbuf); err != nil {
			break
		}
		var h PacketHeader
		if err := json.Unmarshal(hbuf, &h); err != nil {
			break
		}
		var body []byte
		if h.BodyLen > 0 {
			if h.BodyLen > maxBodyBytes {
				break
			}
			body = make([]byte, h.BodyLen)
			if _, err := io.ReadFull(reader, body); err != nil {
				break
			}
		}

		if !authed {
			// 密钥相同即配对成功：不再有"陌生设备需本机确认"这一步，
			// 谁能收到剪贴板完全由密钥决定，所以要改成自己的密钥。
			if h.Type == PacketAuth && h.Token == getToken() {
				n.mu.Lock()
				p := n.peers[ip]
				current := p != nil && p.conn == conn
				became := false
				if current {
					became = !p.authed
					p.authed = true
					p.lastRecv = time.Now()
					if p.name == "" {
						p.name = h.DeviceName
					}
				}
				n.mu.Unlock()
				// 两端同时发现彼此时会各自拨号，形成同一对 IP 之间的两条连接，
				// 后注册的那条把前一条替换掉。旧连接的 readLoop 还会收到 auth，
				// 若照着往下走就会把"已被替换的连接"当成在线、并且重复发一次
				// "已连接/已配对"通知。这里直接断开这条多余的连接。
				if !current {
					log.Printf("认证通过但连接已被新连接替换，关闭多余连接: %s", ip)
					break
				}
				authed = true
				if became && n.onPeerChange != nil {
					n.onPeerChange(ip, h.DeviceName, true)
				}
				log.Println("认证通过:", ip, h.DeviceName)
				continue
			}
			log.Println("认证失败，断开:", ip)
			break
		}

		// 已认证会话里仍然逐包校验密钥：对端改了密钥后，继续收它的包没有意义。
		// 原来这里是 continue（丢包但留着连接），结果本机菜单一直显示"已连接"、
		// 收发却全是空的，要等 90s 心跳超时才自愈 —— 那种"假在线"最难排查。
		// 直接断开，让它按新密钥重新认证一次。
		if h.Token != getToken() {
			log.Printf("对端密钥不符，断开重连: %s（对端可能刚改过密钥）", ip)
			break
		}
		// 心跳判定：记录最近一次收到该对端任意包的时间
		n.mu.Lock()
		if p := n.peers[ip]; p != nil && p.conn == conn {
			p.lastRecv = time.Now()
			if h.Type == PacketPong {
				p.pongSeen = true
			}
		}
		n.mu.Unlock()
		if n.onPacket != nil {
			n.onPacket(ip, &h, body)
		}
	}
	n.dropPeer(ip, conn)
}

// RefreshConnections 配对密钥变更后重建所有连接。
// 已认证的会话是用旧密钥建立的，若留着会出现"菜单显示在线、但包因密钥不符被丢弃"
// 的假在线状态，所以先全部断开，再按已知 IP 主动重拨并触发一次广播，
// 让双方用新密钥重新认证；对端若仍是旧密钥会认证失败（日志可见），改完自动恢复。
func (n *Network) RefreshConnections() {
	type target struct {
		ip   string
		conn net.Conn
	}
	n.mu.RLock()
	targets := make([]target, 0, len(n.peers))
	for ip, p := range n.peers {
		targets = append(targets, target{ip: ip, conn: p.conn})
	}
	n.mu.RUnlock()

	for _, t := range targets {
		n.dropPeer(t.ip, t.conn)
	}
	// 立即广播一次，让对端也尽快用新密钥回连
	select {
	case n.helloTrigger <- struct{}{}:
	default:
	}
	for _, t := range targets {
		go n.dialPeer(t.ip)
	}
}

// dropPeer 按 (ip, conn) 匹配删除；若该 ip 的连接已被新连接替换则不动，避免误删新连接
func (n *Network) dropPeer(ip string, conn net.Conn) {
	n.mu.Lock()
	p, ok := n.peers[ip]
	if ok && p.conn == conn {
		delete(n.peers, ip)
		name := p.name
		n.mu.Unlock()
		if n.onPeerChange != nil && name != "" {
			n.onPeerChange(ip, name, false)
		}
	} else {
		n.mu.Unlock()
	}
	if conn != nil {
		conn.Close()
	}
}

// Send 广播到所有已认证的对端
func (n *Network) Send(h PacketHeader, body []byte) {
	h.DeviceName = n.deviceName
	h.Token = getToken()
	h.BodyLen = len(body)
	pkt := PackPacket(h, body)

	n.mu.RLock()
	var targets []*peer
	for _, p := range n.peers {
		if p.authed {
			targets = append(targets, p)
		}
	}
	n.mu.RUnlock()

	for _, p := range targets {
		if p.conn == nil {
			continue
		}
		p.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := p.conn.Write(pkt); err != nil {
			p.conn.SetWriteDeadline(time.Time{})
			n.dropPeer(p.ip, p.conn)
			continue
		}
		p.conn.SetWriteDeadline(time.Time{})
	}
}

// PeerOnline 是否存在已认证对端
func (n *Network) PeerOnline() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.peers {
		if p.authed {
			return true
		}
	}
	return false
}
