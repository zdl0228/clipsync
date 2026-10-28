package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/sqweek/dialog"
)

// ---------- 文件发送 ----------

// sendFileTo 分块读取并发送文件到指定对端；返回是否整份发出
// silent 为 true 时不发单条"发送完成"通知（批量发送时由上层统一汇总）
func sendFileTo(ip string, path string, silent bool) bool {
	return sendFileToRel(ip, path, "", silent)
}

// sendFileToRel 同 sendFileTo，额外携带相对路径（文件夹发送时用来在
// 对端重建目录结构）；rel 为空表示单个文件，落在桌面根目录。
//
// 返回值用于批量场景统计成功数：原来无返回值，一个文件都发不出去
// （权限、断链符号链接、连接中途断开）也会在上层报"已发送 N 个"。
// silent 为真时不弹模态错误框 —— 批量发送时每个坏文件弹一个框会
// 连弹十几个、并把整个发送流程卡住，只记日志即可。
func sendFileToRel(ip string, path string, rel string, silent bool) bool {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sendFileTo panic: %v", r)
		}
	}()
	if !netw.IsPeerAuthed(ip) {
		return false
	}
	fail := func(format string, args ...any) {
		log.Printf("发送失败 %s: "+format, append([]any{path}, args...)...)
		if !silent {
			dialog.Message("文件发送失败：%s\n\n"+format,
				append([]any{filepath.Base(path)}, args...)...).Error()
		}
	}
	f, err := os.Open(path)
	if err != nil {
		fail("%v", err)
		return false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		fail("%v", err)
		return false
	}
	if stat.IsDir() {
		// 走到这里说明调用方判断错了类型，交给文件夹路径处理
		return false
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
	var sent int64
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
		sent += int64(n)

		// 断线中止
		if !netw.IsPeerAuthed(ip) {
			fail("连接已断开")
			return false
		}

		pct := (i + 1) * 100 / chunkCount
		progressSet(fmt.Sprintf("发送中: %s %d%%", name, pct))
		if err != nil {
			fail("%v", err)
			return false
		}
	}
	// 发送过程中文件被改动/截断：字节数对不上就别报成功（对端会收到半截）
	if sent != size {
		fail("文件在发送过程中被修改（已发 %d / %d 字节）", sent, size)
		return false
	}

	log.Printf("文件发送完成: %s (%d bytes)", name, size)
	if !silent {
		notify("ClipSync", fmt.Sprintf("文件发送完成：%s（%d 字节）", name, size))
	}
	return true
}

// sendFolderTo 递归收集文件夹内所有文件，逐个发送到指定对端（复用单文件协议，兼容旧版）
// 每个文件都带上相对路径（含最外层文件夹名），接收端据此重建目录结构；
// 空目录单独发一条 file_dir 包，否则只有文件的目录会把空目录丢掉。
// quietFinal 为 true 时不发本文件夹汇总通知（整体批量时由上层统一汇总）
// 返回 (成功文件数, 失败文件数)，供调用方汇总成一条准确的提示
func sendFolderTo(ip string, dir string, quietFinal bool) (ok, failed int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sendFolderTo panic: %v", r)
		}
	}()
	if !netw.IsPeerAuthed(ip) {
		return 0, 0
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
		return 0, 0
	}
	if len(files) == 0 && len(dirs) == 0 {
		// 完全空的文件夹：也得让对端建出这个目录，否则"发过去了"却什么都没有
		netw.SendTo(ip, PacketHeader{Type: PacketFileDir, RelPath: relPathFor(clean, clean)}, nil)
		notify("ClipSync", fmt.Sprintf("已发送空文件夹：%s", top))
		return 0, 0
	}
	log.Printf("开始发送文件夹: %s (共 %d 个文件, %d 个子目录)", clean, len(files), len(dirs))

	// 先建目录：空目录只能靠这一步传递，同时让文件落位时目录一定存在
	for _, dp := range dirs {
		netw.SendTo(ip, PacketHeader{Type: PacketFileDir, RelPath: relPathFor(clean, dp)}, nil)
	}
	for _, fp := range files {
		// 文件夹内逐个文件静默，避免通知轰炸；失败只记日志，最后统一汇总
		if sendFileToRel(ip, fp, relPathFor(clean, fp), true) {
			ok++
		} else {
			failed++
		}
		if !netw.IsPeerAuthed(ip) {
			// 断线后剩下的文件必然全失败，直接记账跳出，别一个个试
			failed += len(files) - ok - failed
			log.Printf("文件夹发送中断：连接已断开（已发 %d/%d）", ok, len(files))
			break
		}
	}
	log.Printf("文件夹发送完成: %s (成功 %d 个, 失败 %d 个)", clean, ok, failed)
	if !quietFinal {
		switch {
		case ok == 0 && failed > 0:
			notify("ClipSync", fmt.Sprintf("文件夹发送失败：%s（%d 个文件都没发出去）", top, failed))
		case failed > 0:
			notify("ClipSync", fmt.Sprintf("文件夹部分发送：%s（成功 %d 个，失败 %d 个）", top, ok, failed))
		default:
			notify("ClipSync", fmt.Sprintf("文件夹发送完成：%s（%d 个文件）", top, ok))
		}
	}
	return ok, failed
}

// notifyBatchResult 汇总一次批量发送的结果（托盘多选、右键发送共用）。
// 原来无条件报"已发送 N 个文件/文件夹"——断线或权限错误时一个都没发出去也照样报成功。
func notifyBatchResult(prefix string, ok, failed int) {
	switch {
	case ok == 0 && failed > 0:
		notify("ClipSync", fmt.Sprintf("%s失败：%d 个文件都没发出去", prefix, failed))
	case failed > 0:
		notify("ClipSync", fmt.Sprintf("%s %d 个，%d 个失败", prefix, ok, failed))
	case ok > 0:
		notify("ClipSync", fmt.Sprintf("%s %d 个文件/文件夹", prefix, ok))
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
