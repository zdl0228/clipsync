package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sqweek/dialog"
)

// ---------- 文件发送 ----------

// onDropFiles 拖文件/文件夹到菜单栏图标时触发：
// 只有 1 台设备在线 → 直接发送；多台在线 → 弹选择框勾选目标设备后发送
func onDropFiles(files []string) {
	if len(files) == 0 {
		return
	}
	var targets []PeerInfo
	for _, p := range netw.Peers() {
		if p.Authed {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		notify("ClipSync", "没有可接收文件的设备在线")
		return
	}
	var chosen []PeerInfo
	if len(targets) == 1 {
		chosen = targets
	} else {
		opts := make([]string, len(targets))
		for i, t := range targets {
			opts[i] = t.Name + " (" + t.IP + ")"
		}
		sel := choosePeers(opts)
		if len(sel) == 0 {
			log.Println("拖放发送：用户未选择目标设备，取消")
			return
		}
		ipRe := regexp.MustCompile(`\(([0-9.]+)\)`)
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
	}
	if len(chosen) == 0 {
		log.Println("拖放发送：没有匹配到目标设备，取消")
		return
	}
	log.Printf("拖放接收 %d 个文件/文件夹，发送给 %d 台设备", len(files), len(chosen))
	for _, t := range chosen {
		for _, p := range files {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				sendFolderTo(t.IP, p, true)
			} else if st, err := os.Stat(p); err == nil && !st.IsDir() {
				sendFileTo(t.IP, p, true)
			} else {
				log.Printf("拖放文件不存在，跳过: %s", p)
			}
		}
	}
	notify("ClipSync", fmt.Sprintf("已把 %d 个文件/文件夹发送给 %d 台设备", len(files), len(chosen)))
}

// sendFileTo 分块读取并发送文件到指定对端；断线时中止并提示
// silent 为 true 时不发单条"发送完成"通知（批量发送时由上层统一汇总）
func sendFileTo(ip string, path string, silent bool) {
	sendFileToRel(ip, path, "", silent)
}

// sendFileToRel 同 sendFileTo，额外携带相对路径（文件夹发送时用来在
// 对端重建目录结构）；rel 为空表示单个文件，落在桌面根目录。
func sendFileToRel(ip string, path string, rel string, silent bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sendFileTo panic: %v", r)
		}
	}()
	if !netw.IsPeerAuthed(ip) {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("打开文件失败: %v", err)
		dialog.Message("打开文件失败：%v", err).Error()
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		log.Printf("读取文件信息失败: %v", err)
		return
	}
	size := stat.Size()
	name := filepath.Base(path)
	chunkCount := int((size + int64(FileChunkSize) - 1) / int64(FileChunkSize))
	if chunkCount == 0 {
		chunkCount = 1
	}

	log.Printf("开始发送文件: %s (%d bytes, %d chunks, rel=%q)", name, size, chunkCount, rel)
	netw.SendTo(ip, PacketHeader{
		Type: PacketFileMeta, FileName: name, FileSize: size,
		ChunkCount: chunkCount, RelPath: rel,
	}, nil)

	progressBegin()
	defer progressEnd() // 任何返回都结束该进行中任务
	progressSet(fmt.Sprintf("发送中: %s 0%%", name))

	buf := make([]byte, FileChunkSize)
	for i := 0; i < chunkCount; i++ {
		n, err := f.Read(buf)
		if n == 0 {
			if i == 0 {
				// 空文件：仍发一个空数据块，让接收端完成
				netw.SendTo(ip, PacketHeader{Type: PacketFileData, ChunkIndex: i, ChunkCount: chunkCount}, nil)
			}
			break
		}
		netw.SendTo(ip, PacketHeader{Type: PacketFileData, ChunkIndex: i, ChunkCount: chunkCount}, buf[:n])

		// 断线中止
		if !netw.IsPeerAuthed(ip) {
			log.Println("发送中断：连接已断开")
			dialog.Message("文件发送失败：连接已断开").Error()
			return
		}

		pct := (i + 1) * 100 / chunkCount
		progressSet(fmt.Sprintf("发送中: %s %d%%", name, pct))
		if err != nil {
			break
		}
	}

	log.Printf("文件发送完成: %s (%d bytes)", name, size)
	if !silent {
		notify("ClipSync", fmt.Sprintf("文件发送完成：%s（%d 字节）", name, size))
	}
}

// sendFolderTo 递归收集文件夹内所有文件，逐个发送到指定对端（复用单文件协议，兼容旧版）
// 每个文件都带上相对路径（含最外层文件夹名），接收端据此重建目录结构；
// 空目录单独发一条 file_dir 包，否则只有文件的目录会把空目录丢掉。
// quietFinal 为 true 时不发本文件夹汇总通知（整体批量时由上层统一汇总）
func sendFolderTo(ip string, dir string, quietFinal bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sendFolderTo panic: %v", r)
		}
	}()
	if !netw.IsPeerAuthed(ip) {
		return
	}

	clean := filepath.Clean(dir)
	top := topFolderName(clean)

	var files, dirs []string
	err := filepath.WalkDir(clean, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个子目录没权限就跳过，别让整个文件夹发送失败
			log.Printf("遍历跳过 %s: %v", p, err)
			return nil
		}
		if d.IsDir() {
			if p != clean {
				dirs = append(dirs, p)
			}
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		log.Printf("读取文件夹失败: %v", err)
		dialog.Message("读取文件夹失败：%v", err).Error()
		return
	}
	if len(files) == 0 && len(dirs) == 0 {
		// 完全空的文件夹：也得让对端建出这个目录，否则"发过去了"却什么都没有
		netw.SendTo(ip, PacketHeader{Type: PacketFileDir, RelPath: relPathFor(clean, clean)}, nil)
		notify("ClipSync", fmt.Sprintf("已发送空文件夹：%s", top))
		return
	}
	log.Printf("开始发送文件夹: %s (共 %d 个文件, %d 个子目录)", clean, len(files), len(dirs))

	// 先建目录：空目录只能靠这一步传递，同时让文件落位时目录一定存在
	for _, dp := range dirs {
		netw.SendTo(ip, PacketHeader{Type: PacketFileDir, RelPath: relPathFor(clean, dp)}, nil)
	}
	for _, fp := range files {
		sendFileToRel(ip, fp, relPathFor(clean, fp), true) // 文件夹内逐个文件静默，避免通知轰炸
	}
	log.Printf("文件夹发送完成: %s (共 %d 个文件)", clean, len(files))
	if !quietFinal {
		notify("ClipSync", fmt.Sprintf("文件夹发送完成：%s（%d 个文件）", top, len(files)))
	}
}

// topFolderName 取文件夹自己的名字，作为对端保存时的顶层目录名
func topFolderName(clean string) string {
	top := filepath.Base(clean)
	if top == "" || top == "." || top == string(filepath.Separator) {
		return "folder" // 直接发根目录这种极端情况，给个兜底名字
	}
	return top
}

// relPathFor 把 root 里的某个路径换算成"以 root 的父目录为基准"的相对路径，
// 最外层文件夹名自然包含在内（root=/a/proj, p=/a/proj/sub/x → proj/sub/x）。
// 换算失败（不同卷、符号链接指向外面）时退回"顶层名 + 文件名"，至少不丢文件。
func relPathFor(root, p string) string {
	top := topFolderName(root)
	rel, err := filepath.Rel(filepath.Dir(root), p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		rel = filepath.Join(top, filepath.Base(p))
	}
	return filepath.ToSlash(rel)
}
