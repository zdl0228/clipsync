//go:build darwin

package main

/*
#cgo LDFLAGS: -framework UserNotifications -framework Foundation
#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

// 请求通知权限（首次调用会弹系统授权框）
static void notifyCenterRequestAuth(void) {
    UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
    [c requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                      completionHandler:^(BOOL granted, NSError *error) {}];
}

// 发送通知：图标自动使用当前 App 的 bundle 图标（ClipSync 放大剪贴板版）
static void notifyCenterSend(const char *title, const char *body) {
    UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
    UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
    content.title = [NSString stringWithUTF8String:title];
    content.body = [NSString stringWithUTF8String:body];
    content.sound = [UNNotificationSound defaultSound];
    UNNotificationRequest *req = [UNNotificationRequest
        requestWithIdentifier:[[NSUUID UUID] UUIDString]
                      content:content
                      trigger:nil];
    [c addNotificationRequest:req withCompletionHandler:nil];
}
*/
import "C"

import (
	"sync"
	"unsafe"
)

var notifyAuthOnce sync.Once

// notifyOS 用系统原生 UNUserNotificationCenter 发送通知，
// 横幅图标自动跟随 ClipSync 的程序图标（无需第三方工具）
func notifyOS(title, msg string) error {
	notifyAuthOnce.Do(func() {
		C.notifyCenterRequestAuth()
	})
	t := C.CString(title)
	m := C.CString(msg)
	C.notifyCenterSend(t, m)
	C.free(unsafe.Pointer(t))
	C.free(unsafe.Pointer(m))
	return nil
}
