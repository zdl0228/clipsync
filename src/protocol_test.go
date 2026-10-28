package main

import (
	"bytes"
	"testing"
)

// TestUnpackPacketRejectsBadLength 覆盖 2026-10-28 全量审查发现的远程崩溃：
// 4 字节长度是网络来的 32 位值，旧实现用 `uint32(len(data)) < 4+hlen` 判断，
// 0xFFFFFFFC 会让 4+hlen 回绕成 0，检查被绕过，随后 data[4:4+hlen] 直接 panic。
// UDP 监听没有 recover，所以任意一台同网段主机发 4 个字节就能打掉进程。
func TestUnpackPacketRejectsBadLength(t *testing.T) {
	cases := map[string][]byte{
		"0xFFFFFFFC（4+len 回绕）": {0xFF, 0xFF, 0xFF, 0xFC},
		"0xFFFFFFFF":           {0xFF, 0xFF, 0xFF, 0xFF},
		"超过 maxHeaderBytes":    {0x7F, 0xFF, 0xFF, 0xFF},
		"长度为 0":                {0x00, 0x00, 0x00, 0x00},
		"声明 16 字节但无内容":         {0x00, 0x00, 0x00, 0x10},
		"不足 4 字节":              {0x00, 0x01},
	}
	for name, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: UnpackPacket panic（这正是不该发生的）: %v", name, r)
				}
			}()
			if _, _, err := UnpackPacket(c); err == nil {
				t.Errorf("%s: 应当返回错误，实际 nil", name)
			}
		}()
	}
}

// TestPackUnpackRoundTrip 正常的包仍应完整往返（含中文与空 body）
func TestPackUnpackRoundTrip(t *testing.T) {
	for _, body := range [][]byte{nil, {}, []byte("剪贴板正文 with ASCII")} {
		pkt := PackPacket(PacketHeader{
			Type:     PacketClipboard,
			MimeType: "text/plain",
			BodyLen:  len(body),
		}, body)
		h, got, err := UnpackPacket(pkt)
		if err != nil {
			t.Fatalf("UnpackPacket 失败: %v", err)
		}
		if h.Type != PacketClipboard || h.MimeType != "text/plain" {
			t.Fatalf("头部不一致: %+v", h)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("body 不一致: got=%q want=%q", got, body)
		}
	}
}
