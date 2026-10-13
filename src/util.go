package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"

	"github.com/gen2brain/beeep"
)

// notify 发送系统通知（成功/完成类消息用被动通知，不弹确认框）
func notify(title, msg string) {
	if err := beeep.Notify(title, msg, ""); err != nil {
		log.Printf("发送通知失败: %v", err)
	}
}

func osHostname() (string, error) {
	return os.Hostname()
}

// fingerprint 生成剪贴板内容标识，用于防回环判断
func fingerprint(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) < 64 {
		return string(body)
	}
	h := fnv.New32a()
	h.Write(body)
	return fmt.Sprintf("%d:%x", len(body), h.Sum32())
}
