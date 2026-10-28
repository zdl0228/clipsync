package main

import (
	"bytes"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestChunkedFileTransfer(t *testing.T) {
	// 源数据：超过一个分块，模拟真实文件
	src := make([]byte, FileChunkSize*3+12345)
	for i := range src {
		src[i] = byte(i*29 + 7)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type pkt struct {
		h    *PacketHeader
		body []byte
	}
	recvCh := make(chan pkt, 64)

	recv := NewNetwork("recv")
	recv.OnPacket(func(ip string, h *PacketHeader, body []byte) {
		recvCh <- pkt{h, append([]byte(nil), body...)}
	})

	// server 端：接受 + TLS 握手 + 发 auth，交给 readLoop 读取
	serverCh := make(chan net.Conn, 1)
	go func() {
		raw, _ := ln.Accept()
		cfg := tlsConfig()
		if cfg == nil {
			serverCh <- nil
			return
		}
		conn := tls.Server(raw, cfg)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err := conn.Handshake(); err != nil {
			conn.Close()
			serverCh <- nil
			return
		}
		conn.SetDeadline(time.Time{})
		conn.Write(recv.authPacket())
		serverCh <- conn
	}()

	rawC, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg := tlsConfig()
	if cfg == nil {
		t.Fatal("tls config nil")
	}
	client := tls.Client(rawC, cfg)
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := client.Handshake(); err != nil {
		t.Fatal("client handshake:", err)
	}
	client.SetDeadline(time.Time{})
	// client 发自己的 auth（token 一致即通过）
	client.Write(recv.authPacket())
	serverConn := <-serverCh
	if serverConn == nil {
		t.Fatal("server conn nil")
	}

	// 注册对端并启动 readLoop（readLoop 会先读到 client 的 auth 完成认证）
	serverIP := "127.0.0.2"
	recv.mu.Lock()
	recv.peers[serverIP] = &peer{ip: serverIP, conn: serverConn, authed: false}
	recv.mu.Unlock()
	go recv.readLoop(serverIP, serverConn)

	// 发送端
	send := NewNetwork("send")
	clientIP := "127.0.0.3"
	send.mu.Lock()
	send.peers[clientIP] = &peer{ip: clientIP, conn: client, authed: true}
	send.mu.Unlock()

	// 发送 meta
	name := "test.bin"
	chunkCount := (len(src) + FileChunkSize - 1) / FileChunkSize
	send.Send(PacketHeader{Type: PacketFileMeta, FileName: name, FileSize: int64(len(src)), ChunkCount: chunkCount}, nil)

	// 分块发送（连续快速，制造粘包）
	for i := 0; i < chunkCount; i++ {
		start := i * FileChunkSize
		end := start + FileChunkSize
		if end > len(src) {
			end = len(src)
		}
		send.Send(PacketHeader{Type: PacketFileData, ChunkIndex: i, ChunkCount: chunkCount}, src[start:end])
	}

	// 接收 meta
	mp := <-recvCh
	if mp.h.Type != PacketFileMeta {
		t.Fatalf("期望 meta, 得到 %s", mp.h.Type)
	}
	if mp.h.FileSize != int64(len(src)) || mp.h.ChunkCount != chunkCount {
		t.Fatalf("meta 字段错误: size=%d want=%d count=%d want=%d",
			mp.h.FileSize, len(src), mp.h.ChunkCount, chunkCount)
	}

	// 接收 data 块并重组
	got := bytes.NewBuffer(nil)
	for i := 0; i < chunkCount; i++ {
		dp := <-recvCh
		if dp.h.Type != PacketFileData {
			t.Fatalf("期望 data, 得到 %s", dp.h.Type)
		}
		if dp.h.ChunkIndex != i {
			t.Fatalf("块顺序错误: got %d want %d", dp.h.ChunkIndex, i)
		}
		got.Write(dp.body)
	}
	if !bytes.Equal(got.Bytes(), src) {
		t.Fatalf("数据不一致: got=%d src=%d", got.Len(), len(src))
	}
}

// TestSafeFileName 验证接收文件名清洗（防路径穿越）
func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":   "report.pdf",
		"../evil.txt":  "evil.txt",
		"/etc/passwd":  "passwd",
		"a/../b/c.txt": "c.txt",
		"..":           "received_file",
		"":             "received_file",
		".":            "received_file",
		// Windows 保留设备名：加前缀避开，否则 Windows 端保存必失败
		"con.txt":  "_con.txt",
		"COM1":     "_COM1",
		"nul.json": "_nul.json",
	}
	for in, want := range cases {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSafeRelDir 验证文件夹传输的相对路径清洗：必须挡住目录穿越，
// 同时保留正常的嵌套结构
func TestSafeRelDir(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"proj/a.txt":         "proj",
		"proj/sub/dir/a.txt": filepath.Join("proj", "sub", "dir"),
		// ".." 段整段丢弃，剩下的段只能落在桌面之内，所以
		// "../etc/passwd" 实际存成 桌面/etc/passwd，不会真的写到 /etc
		"../etc/passwd":        "etc",
		"proj/../../etc/a.txt": filepath.Join("proj", "etc"),
		"/abs/a.txt":           "abs",
		"proj\\sub\\a.txt":     filepath.Join("proj", "sub"), // Windows 分隔符
		"proj//a.txt":          "proj",
		"./proj/./a.txt":       "proj",
		"proj/con.txt":         "proj",
		"a.txt":                "",
	}
	for in, want := range cases {
		if got := safeRelDir(in); got != want {
			t.Errorf("safeRelDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReservePathAtomic 验证并发占用同名目标时不会互相覆盖
func TestReservePathAtomic(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	paths := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := reservePath(dir, "report.pdf")
			if err != nil {
				t.Errorf("reservePath: %v", err)
				return
			}
			paths <- p
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for p := range paths {
		if seen[p] {
			t.Errorf("两个并发接收拿到了同一个路径: %s", p)
		}
		seen[p] = true
	}
	if len(seen) != n {
		t.Errorf("期望 %d 个不同路径，实际 %d", n, len(seen))
	}
	// 已有文件时不能再返回同名路径
	if _, err := os.Stat(filepath.Join(dir, "report.pdf")); err != nil {
		t.Errorf("首个路径应已创建占位文件: %v", err)
	}
}
