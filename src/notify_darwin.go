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

// 查询当前授权状态：0=未决定 1=已拒绝 2=已允许 3=临时允许 -1=查询超时
static int notifyCenterStatus(void) {
    __block int st = -1;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [[UNUserNotificationCenter currentNotificationCenter]
        getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *s) {
            st = (int)s.authorizationStatus;
            dispatch_semaphore_signal(sem);
        }];
    dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, (int64_t)(2 * NSEC_PER_SEC)));
    return st;
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

// notifyPrepare 在启动时就请求授权。
// 原来的做法是第一次发通知时才请求，而 requestAuthorization 是异步的、
// 紧接着的 addNotificationRequest 必然赶不上，第一条通知白白丢掉；
// 若那次授权框被忽略，之后就再也没有通知。所以提前到启动时。
func notifyPrepare() {
	notifyAuthOnce.Do(func() {
		C.notifyCenterRequestAuth()
	})
}

// notifyPermission 返回当前通知授权状态的中文描述，供菜单/日志展示
func notifyPermission() string {
	switch int(C.notifyCenterStatus()) {
	case 0:
		return "未授权（系统还没问过）"
	case 1:
		return "已拒绝（需到 系统设置 → 通知 → ClipSync 打开）"
	case 2:
		return "已允许"
	case 3:
		return "临时允许"
	}
	return "查询超时"
}

// notifyOS 用系统原生 UNUserNotificationCenter 发送通知，
// 横幅图标自动跟随 ClipSync 的程序图标（无需第三方工具）
func notifyOS(title, msg string) error {
	notifyPrepare()
	t := C.CString(title)
	m := C.CString(msg)
	C.notifyCenterSend(t, m)
	C.free(unsafe.Pointer(t))
	C.free(unsafe.Pointer(m))
	return nil
}
