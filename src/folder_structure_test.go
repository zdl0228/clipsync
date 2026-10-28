package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 临时探针：验证文件夹传输的"相对路径 → 桌面子目录"整条接收链路
func TestFolderStructureProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := desktopDir(); got != filepath.Join(home, "Desktop") {
		t.Fatalf("desktopDir=%s", got)
	}

	// 发送端算相对路径
	root := filepath.Join(home, "work", "proj")
	fp := filepath.Join(root, "sub", "a.txt")
	rel := relPathFor(root, fp)
	t.Logf("relPathFor(%s) = %s", fp, rel)
	if rel != "proj/sub/a.txt" {
		t.Fatalf("relPathFor 结果不对: %s", rel)
	}

	// 接收端：先目录包，再文件包
	handleFileDir("1.2.3.4", &PacketHeader{Type: PacketFileDir, RelPath: "proj/empty"})
	body := []byte("hello folder")
	handleFileMeta("1.2.3.4", &PacketHeader{
		Type: PacketFileMeta, FileName: "a.txt", FileSize: int64(len(body)),
		ChunkCount: 1, RelPath: rel,
	})
	handleFileData("1.2.3.4", &PacketHeader{Type: PacketFileData, ChunkIndex: 0, ChunkCount: 1}, body)

	time.Sleep(600 * time.Millisecond)

	want := filepath.Join(home, "Desktop", "proj", "sub", "a.txt")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("期望文件不存在: %v", err)
	}
	if string(data) != string(body) {
		t.Fatalf("内容不符: %q", data)
	}
	if st, err := os.Stat(filepath.Join(home, "Desktop", "proj", "empty")); err != nil || !st.IsDir() {
		t.Fatalf("空目录没建出来: %v", err)
	}
	t.Log("目录结构与内容都正确")

	// 同名再来一次：不能覆盖，应该变成 a(1).txt
	handleFileMeta("1.2.3.4", &PacketHeader{
		Type: PacketFileMeta, FileName: "a.txt", FileSize: int64(len(body)),
		ChunkCount: 1, RelPath: rel,
	})
	handleFileData("1.2.3.4", &PacketHeader{Type: PacketFileData, ChunkIndex: 0, ChunkCount: 1}, body)
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(home, "Desktop", "proj", "sub", "a(1).txt")); err != nil {
		t.Fatalf("同名文件没有自动改名: %v", err)
	}
	t.Log("同名文件自动改成 a(1).txt，未覆盖")
}

// TestReceiveWithoutDesktop 桌面目录本身不存在时（新账户 / 被删 / 外置卷卸载）
// 接收要能自己把目录建出来，而不是直接失败
func TestReceiveWithoutDesktop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := os.Stat(filepath.Join(home, "Desktop")); !os.IsNotExist(err) {
		t.Fatalf("前提不成立：Desktop 不该存在")
	}
	body := []byte("no desktop")
	handleFileMeta("9.9.9.9", &PacketHeader{
		Type: PacketFileMeta, FileName: "x.txt", FileSize: int64(len(body)), ChunkCount: 1,
	})
	handleFileData("9.9.9.9", &PacketHeader{Type: PacketFileData, ChunkIndex: 0, ChunkCount: 1}, body)
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(home, "Desktop", "x.txt")); err != nil {
		t.Fatalf("桌面不存在时应自动创建: %v", err)
	}
}
