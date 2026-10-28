package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadIpcLine(t *testing.T) {
	// 正常一行（含结尾换行）
	r := bufio.NewReaderSize(strings.NewReader("hello\nworld\n"), 64*1024)
	if s, err := readIpcLine(r); err != nil || s != "hello" {
		t.Fatalf("第一行 = %q, %v", s, err)
	}
	if s, err := readIpcLine(r); err != nil || s != "world" {
		t.Fatalf("第二行 = %q, %v", s, err)
	}

	// 最后一行没有换行符也要能读出来（helper 半关闭后可能没有 \n）
	r = bufio.NewReaderSize(strings.NewReader("tail"), 64*1024)
	if s, err := readIpcLine(r); err != nil || s != "tail" {
		t.Fatalf("末行 = %q, %v", s, err)
	}

	// 超长行必须在读完之前就被拒绝：ReadString 的实现会先把整行读进内存，
	// 本机任意进程都能用它把内存打满
	long := strings.Repeat("x", maxIpcLine+1) + "\n"
	r = bufio.NewReaderSize(strings.NewReader(long), 64*1024)
	if _, err := readIpcLine(r); err == nil {
		t.Fatal("超长行应返回错误")
	}

	// 超过缓冲区（64KB）的行同样拒绝，不能一直读下去
	huge := strings.Repeat("y", 200*1024) + "\n"
	r = bufio.NewReaderSize(strings.NewReader(huge), 64*1024)
	if _, err := readIpcLine(r); err == nil {
		t.Fatal("超过缓冲区的行应返回错误")
	}
}
