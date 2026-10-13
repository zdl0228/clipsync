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
	PacketFileData  = "file_data"

	// 文件分块大小
	FileChunkSize = 256 * 1024
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

func UnpackPacket(data []byte) (*PacketHeader, []byte, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("packet too short")
	}
	hlen := binary.BigEndian.Uint32(data[:4])
	if uint32(len(data)) < 4+hlen {
		return nil, nil, fmt.Errorf("incomplete packet")
	}
	var h PacketHeader
	if err := json.Unmarshal(data[4:4+hlen], &h); err != nil {
		return nil, nil, err
	}
	return &h, data[4+hlen:], nil
}
