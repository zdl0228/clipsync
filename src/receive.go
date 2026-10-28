package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 按对端 IP 隔离的接收状态，避免多设备同时收文件互相中断
var (
	recvMu sync.Mutex
	recvs  = map[string]*recvState{}
)

// recvState 文件接收状态（临时文件，接收完成直接保存到桌面）
type recvState struct {
	m          sync.Mutex
	tmpFile    *os.File
	tmpPath    string
	fileName   string
	total      int64
	got        int64
	count      int
	chunkCount int
}

// ---------- 文件接收 ----------

// desktopDir 返回桌面路径（接收文件默认保存到这里）
func desktopDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

// safeFileName 清洗对端传来的文件名：只保留文件名部分，去掉目录/穿越段，防止路径穿越；
// 并移除 Windows 非法字符，避免 mac→Windows 发送后目标端保存失败。
func safeFileName(name string) string {
	n := filepath.Base(name)
	if n == "" || n == "." || n == ".." {
		return "received_file"
	}
	n = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 0x20 { // 控制字符
			return '_'
		}
		return r
	}, n)
	n = strings.TrimRight(n, ". ") // Windows 文件名不能以空格或点结尾
	if n == "" {
		return "received_file"
	}
	// Windows 的保留设备名（CON/PRN/AUX/NUL/COM1..9/LPT1..9）不能当文件名，
	// 带扩展名也不行（CON.txt 同样打不开）。mac 发过来一个叫 con.txt 的文件时，
	// 不收这道处理会在 Windows 上保存失败，用户只会看到"没收到"。
	switch strings.ToUpper(strings.TrimSuffix(n, filepath.Ext(n))) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		n = "_" + n
	}
	return n
}

// handleFileMeta 收到元信息：写临时文件，直接自动接收，完成后保存到桌面。
// 按对端 IP 隔离接收状态，多设备同时发文件互不干扰。
func handleFileMeta(ip string, h *PacketHeader) {
	// 元信息必须自洽：负大小、0 块、超过上限的声明一律拒绝。
	// 少了这道校验，chunkCount=0 会让"收到第一块即完成"成立（截断文件），
	// 而超大的 total 会让接收状态和临时文件一直挂着。
	if h.FileSize < 0 || h.FileSize > maxRecvFileBytes || h.ChunkCount <= 0 {
		log.Printf("拒绝异常的 file_meta: from=%s size=%d chunks=%d", ip, h.FileSize, h.ChunkCount)
		notify("ClipSync", "拒绝接收异常文件（元信息不合法）")
		return
	}
	// 该对端上一个接收未完成则丢弃
	clearRecv(ip)

	tmp, err := os.CreateTemp("", "clipsync-*")
	if err != nil {
		log.Printf("创建临时文件失败: %v", err)
		notify("ClipSync", "接收文件失败：无法创建临时文件")
		return
	}
	rs := &recvState{
		tmpFile:    tmp,
		tmpPath:    tmp.Name(),
		fileName:   safeFileName(h.FileName),
		total:      h.FileSize,
		chunkCount: h.ChunkCount,
	}
	recvMu.Lock()
	recvs[ip] = rs
	recvMu.Unlock()

	progressBegin()
	progressSet(fmt.Sprintf("接收中: %s 0%%", safeFileName(h.FileName)))
	log.Printf("开始接收文件: from=%s %s (%d bytes)", ip, safeFileName(h.FileName), h.FileSize)
}

// handleFileData 收到数据块，写入临时文件；完成时异步移动/清理
func handleFileData(ip string, h *PacketHeader, body []byte) {
	recvMu.Lock()
	rs := recvs[ip]
	recvMu.Unlock()
	if rs == nil {
		log.Println("收到数据块但没有接收目标")
		return
	}
	var werr error
	rs.m.Lock()
	// 只写入尚缺的字节数，防止对端多发数据块导致超出声明大小、写入多余字节
	if need := rs.total - rs.got; need > 0 {
		if int64(len(body)) > need {
			body = body[:int(need)]
		}
		n, err := rs.tmpFile.Write(body)
		rs.got += int64(n)
		if err != nil {
			werr = err
		}
	}
	rs.count++
	// 完成条件：字节数达标，或块数达标（空文件/对端提前结束时的兜底）。
	// 快照成局部变量，避免下面在锁外读这些字段（clearRecv 可能并发改写状态）。
	got, total, count := rs.got, rs.total, rs.count
	done := got >= total || count >= rs.chunkCount
	short := done && got < total // 块数够了但字节不够：对端提前结束，文件不完整
	pct := 0
	if rs.chunkCount > 0 {
		pct = count * 100 / rs.chunkCount
		if pct > 100 {
			pct = 100
		}
	}
	rs.m.Unlock()

	progressSet(fmt.Sprintf("接收中: %s %d%%", rs.fileName, pct))

	if werr != nil {
		// 写入失败：丢弃该接收并提示，避免静默产生不完整文件
		log.Printf("接收写入失败: %v", werr)
		clearRecv(ip)
		notify("ClipSync", "文件接收失败：磁盘写入错误")
		return
	}
	if done {
		if short {
			log.Printf("接收不完整：%s 声明 %d 字节，实收 %d 字节（对端可能中断）", rs.fileName, total, got)
		}
		recvMu.Lock()
		if recvs[ip] == rs {
			delete(recvs, ip)
		}
		recvMu.Unlock()
		go finishReceive(rs)
	}
}

// finishReceive 关闭临时文件，保存到桌面
func finishReceive(rs *recvState) {
	rs.tmpFile.Close()
	progressEnd()

	dir := desktopDir()
	final := filepath.Join(dir, avoidOverwrite(rs.fileName))
	if err := os.Rename(rs.tmpPath, final); err != nil {
		log.Printf("重命名到桌面失败: %v", err)
		// 跨文件系统时 Rename 会返回 EXDEV，降级为拷贝兜底，避免文件丢失
		if err2 := copyFile(rs.tmpPath, final); err2 != nil {
			log.Printf("拷贝保存也失败: %v", err2)
			notify("ClipSync", fmt.Sprintf("文件接收失败，保存到桌面出错：%v", err2))
			return
		}
	}
	log.Printf("文件接收完成: %s (%d bytes)", final, rs.total)
	scheduleRecvNotify()
}

// 接收通知汇总：短时间内连续接收多个文件时，只发一条汇总通知，避免轰炸
var recvDone struct {
	mu    sync.Mutex
	count int
	timer *time.Timer
}

// scheduleRecvNotify 记录一个已接收文件并调度 2 秒后的汇总通知；期间再有新文件则重置计时
func scheduleRecvNotify() {
	recvDone.mu.Lock()
	recvDone.count++
	if recvDone.timer != nil {
		recvDone.timer.Stop()
	}
	recvDone.timer = time.AfterFunc(2*time.Second, func() {
		recvDone.mu.Lock()
		n := recvDone.count
		recvDone.count = 0
		recvDone.timer = nil
		recvDone.mu.Unlock()
		if n > 0 {
			notify("ClipSync", fmt.Sprintf("已保存到桌面：%d 个文件", n))
		}
	})
	recvDone.mu.Unlock()
}

// copyFile 拷贝源文件到目标路径，成功后删除源（跨文件系统重命名失败时的兜底）
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}

// clearRecv 丢弃指定对端未完成的接收（按 IP 隔离）
func clearRecv(ip string) {
	recvMu.Lock()
	defer recvMu.Unlock()
	if rs := recvs[ip]; rs != nil {
		rs.m.Lock()
		rs.tmpFile.Close()
		os.Remove(rs.tmpPath)
		rs.m.Unlock()
		delete(recvs, ip)
		progressEnd() // 被丢弃的接收也结束一个进行中任务
	}
}

// avoidOverwrite 若目标文件已存在，自动追加序号避免覆盖
func avoidOverwrite(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := base[:len(base)-len(ext)]
	for i := 1; i <= 999; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s(%d)%s", name, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	// 极端兜底：序号用尽时以时间戳避免死循环
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", name, time.Now().UnixNano(), ext))
}
