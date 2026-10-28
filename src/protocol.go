package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

const (
	UDPPort         = 2718
	TCPPort         = 2719
	PacketHello     = "hello"
	PacketAuth      = "auth"
	PacketClipboard = "clipboard"
	PacketFileMeta  = "file_meta"
	PacketFileDir   = "file_dir" // 只为传递空目录/目录结构，无 body
	PacketFileData  = "file_data"
	PacketPing      = "ping"
	PacketPong      = "pong"

	// 文件分块大小
	FileChunkSize = 256 * 1024

	// 头部 JSON 上限（正常只有几百字节；放宽到 64KB 兼容将来的字段）
	maxHeaderBytes = 64 * 1024
	// 单个包 body 上限：剪贴板图片与文件块都远小于它，超了必定是异常/恶意
	maxBodyBytes = 128 * 1024 * 1024
	// 接收文件大小上限：防止对端（或伪造的 meta）无限写盘把磁盘塞满
	maxRecvFileBytes = 8 * 1024 * 1024 * 1024
	// 剪贴板内容上限：一次复制几百 MB（超大图片、整个文档）时，
	// 打包与写剪贴板都会长时间占用内存，两边都可能被拖到无响应
	maxClipboardBytes = 32 * 1024 * 1024
)

type PacketHeader struct {
	Type       string `json:"type"`
	DeviceName string `json:"deviceName"`
	Token      string `json:"token"`
	MimeType   string `json:"mimeType,omitempty"`
	BodyLen    int    `json:"bodyLen,omitempty"`
	FileName   string `json:"fileName,omitempty"`
	FileSize   int64  `json:"fileSize,omitempty"`
	ChunkIndex int    `json:"chunkIndex,omitempty"`
	ChunkCount int    `json:"chunkCount,omitempty"`
	// RelPath 是发送文件夹时的相对路径（含最外层文件夹名，用 / 分隔），
	// 接收端据此在桌面重建目录结构。空值表示单个文件，直接放桌面。
	// 旧版对端不认识这个字段，会忽略它 → 退化成扁平存放，不会出错。
	RelPath string `json:"relPath,omitempty"`
}

func (h PacketHeader) JSON() []byte {
	b, _ := json.Marshal(h)
	return b
}

func PackPacket(h PacketHeader, body []byte) []byte {
	hb := h.JSON()
	out := make([]byte, 4+len(hb)+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(len(hb)))
	copy(out[4:], hb)
	copy(out[4+len(hb):], body)
	return out
}

// UnpackPacket 解析一个完整包（4 字节头长度 + JSON 头 + body）。
//
// 长度比较必须用 uint64：hlen 是网络来的 32 位值，`4+hlen` 在 uint32 里会回绕，
// 于是 0xFFFFFFFC 这类长度能绕过"包不完整"的检查，紧接着 data[4:4+hlen]
// 就直接越界 panic。UDP 监听是无人值守的，一个 4 字节的畸形包足以打掉整个进程
// （2026-10-28 全量审查发现，已加 maxHeaderBytes 双保险）。
func UnpackPacket(data []byte) (*PacketHeader, []byte, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("packet too short")
	}
	hlen := uint64(binary.BigEndian.Uint32(data[:4]))
	if hlen == 0 || hlen > maxHeaderBytes {
		return nil, nil, fmt.Errorf("bad header length: %d", hlen)
	}
	if uint64(len(data)) < 4+hlen {
		return nil, nil, fmt.Errorf("incomplete packet")
	}
	var h PacketHeader
	if err := json.Unmarshal(data[4:4+hlen], &h); err != nil {
		return nil, nil, err
	}
	return &h, data[4+hlen:], nil
}
