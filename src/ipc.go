package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sqweek/dialog"
)

// ipcPort 主进程与右键菜单 helper 之间的本地回环通信端口。
// helper 是带 --send 参数重新启动的同一 exe，通过该端口把文件路径
// 转发给已运行在托盘的 ClipSync 主进程，由主进程执行真正的发送。
// 仅监听 127.0.0.1，并用 token 校验，避免被本机其他程序滥用。
const ipcPort = 2720

// ipcAddr 本地 IPC 地址
const ipcAddr = "127.0.0.1:2720"

// 单次 IPC 允许的最大路径数（防恶意超长请求）
const ipcMaxPaths = 200

// maxIpcBody 单行（token 或路径）最大长度，防止大包打满内存
const maxIpcLine = 4096

// runSendHelper 右键菜单触发的发送模式入口：
// 连接主进程的 IPC 端口，把路径列表转发过去后退出；主进程未运行时弹窗提示。
// 该分支在 main() 最前面调用，不会启动托盘。
func runSendHelper(paths []string) {
	paths = cleanPaths(paths)
	if len(paths) == 0 {
		return
	}
	conn, err := net.DialTimeout("tcp", ipcAddr, 1*time.Second)
	if err != nil {
		dialog.Message("ClipSync 未运行。\n请先启动 ClipSync，再右键发送文件。").Error()
		log.Printf("右键发送：主进程未运行，无法连接 IPC: %v", err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 首行 token，之后每行一个路径；token 由主进程校验身份
	w := bufio.NewWriter(conn)
	fmt.Fprintln(w, token)
	for _, p := range paths {
		fmt.Fprintln(w, p)
	}
	w.Flush()

	// 等待主进程回执，确认已受理
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err == nil && strings.HasPrefix(reply, "OK") {
		log.Printf("右键发送已受理: %d 个路径", len(paths))
		return
	}
	log.Printf("右键发送：主进程未确认: %v (回执=%q)", err, strings.TrimSpace(reply))
}

// cleanPaths 去掉空串与空白，限制数量
func cleanPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
		if len(out) >= ipcMaxPaths {
			break
		}
	}
	return out
}

// ipcListen 在托盘主进程中启动本地 IPC 监听：
// 校验 token 后，把收到的文件/文件夹路径发送给所有在线已认证设备。
func ipcListen() {
	ln, err := net.Listen("tcp", ipcAddr)
	if err != nil {
		log.Printf("IPC 监听失败（右键发送不可用）: %v", err)
		return
	}
	log.Printf("IPC 监听已启动: %s", ipcAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go ipcHandle(conn)
	}
}

// ipcHandle 处理一次 IPC 连接；只监听回环地址 + token 校验，防护本机误用
func ipcHandle(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReaderSize(conn, 64*1024)
	// 首行必须是正确的 token
	line, err := readIpcLine(reader)
	if err != nil {
		return
	}
	if line != token {
		log.Printf("IPC token 校验失败，拒绝")
		fmt.Fprintln(conn, "ERR:token")
		return
	}

	var paths []string
	for {
		line, err = readIpcLine(reader)
		if err != nil {
			break // EOF 或超时，结束收集
		}
		if line != "" {
			paths = append(paths, line)
			if len(paths) >= ipcMaxPaths {
				break
			}
		}
	}
	if len(paths) == 0 {
		return
	}
	fmt.Fprintln(conn, "OK")
	log.Printf("IPC 收到 %d 个文件/文件夹路径", len(paths))
	sendPathsToAuthed(paths)
}

// readIpcLine 读取一行，限制长度防大包
func readIpcLine(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	s = strings.TrimSpace(s)
	if len(s) > maxIpcLine {
		return "", fmt.Errorf("line too long")
	}
	return s, nil
}

// sendPathsToAuthed 把路径列表发送给目标设备。
// 1 台对端在线 → 直接发送；2 台及以上 → 弹多选框让用户挑选目标（可多选），取消则不发送。
func sendPathsToAuthed(paths []string) {
	targets := pickTargets()
	if len(targets) == 0 {
		notify("ClipSync", "没有可接收文件的设备在线")
		return
	}
	for _, t := range targets {
		for _, p := range paths {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				sendFolderTo(t.IP, p, true)
			} else if err == nil {
				sendFileTo(t.IP, p, true)
			} else {
				log.Printf("右键发送：路径不存在，跳过 %s", p)
			}
		}
	}
	notify("ClipSync", fmt.Sprintf("已把 %d 个文件/文件夹发送给 %d 台设备", len(paths), len(targets)))
}

// pickTargets 选择本次发送的目标设备集合：
// 1 台 → 直接返回；多台 → 弹多选框（可多选），用户取消返回空
func pickTargets() []PeerInfo {
	var targets []PeerInfo
	for _, p := range netw.Peers() {
		if p.Authed {
			targets = append(targets, p)
		}
	}
	if len(targets) <= 1 {
		return targets
	}

	opts := make([]string, len(targets))
	for i, t := range targets {
		name := t.Name
		if name == "" {
			name = t.IP
		}
		opts[i] = name + " (" + t.IP + ")"
	}
	sel := choosePeers(opts)
	if len(sel) == 0 {
		log.Println("右键发送：用户未选择目标设备，取消")
		return nil
	}
	// 从选中项 "名称 (IP)" 中还原 IP，匹配到目标
	ipRe := regexp.MustCompile(`\(([0-9.]+)\)`)
	var chosen []PeerInfo
	for _, s := range sel {
		if m := ipRe.FindStringSubmatch(s); m != nil {
			for _, t := range targets {
				if t.IP == m[1] {
					chosen = append(chosen, t)
					break
				}
			}
		}
	}
	if len(chosen) == 0 {
		log.Println("右键发送：未匹配到目标设备，取消")
		return nil
	}
	return chosen
}
