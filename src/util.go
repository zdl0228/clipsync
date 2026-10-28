package main

import (
	"fmt"
	"hash/fnv"
	"log"
)

// notify 发送系统通知；实现按平台分发（darwin 用系统原生 UNUserNotificationCenter，
// 其余平台用 beeep），保证通知横幅图标跟随程序图标
func notify(title, msg string) {
	if err := notifyOS(title, msg); err != nil {
		log.Printf("发送通知失败: %v", err)
	}
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
