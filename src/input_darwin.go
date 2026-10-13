//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework Foundation
#import <AppKit/AppKit.h>
#import <stdlib.h>
#import <dispatch/dispatch.h>

// SelectPathsNative 由 App 自身进程弹出系统面板，多选文件和文件夹；
// 返回每行一个路径的字符串；用户取消返回 NULL。
// 在 AppKit 主线程运行并激活 App，避免菜单栏应用（无窗口）弹出时面板一闪而过。
static char* SelectPathsNative() {
    __block char *result = NULL;
    dispatch_sync(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            NSOpenPanel *panel = [NSOpenPanel openPanel];
            [panel setTitle:@"选择要发送的文件或文件夹（可多选）"];
            [panel setCanChooseFiles:YES];
            [panel setCanChooseDirectories:YES];
            [panel setAllowsMultipleSelection:YES];
            [panel setPrompt:@"发送"];
            [NSApp activateIgnoringOtherApps:YES];
            if ([panel runModal] == NSModalResponseOK) {
                NSArray *urls = [panel URLs];
                NSMutableString *out = [NSMutableString string];
                for (NSURL *u in urls) {
                    [out appendString:[u path]];
                    [out appendString:@"\n"];
                }
                result = strdup([out UTF8String]);
            }
        }
    });
    return result;
}
*/
import "C"

import (
	"log"
	"os/exec"
	"strings"
	"unsafe"
)

// inputIP 弹出系统输入框获取对端直连 IP；取消或为空返回 ""
func inputIP() string {
	script := `set r to display dialog "输入要对端直连的 IP 地址（如 192.168.1.100）" default answer "" with title "ClipSync 直连" buttons {"取消", "确定"} default button 2`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return ""
	}
	// 输出形如: text returned:192.168.1.100, button returned:确定
	s := string(out)
	if i := strings.Index(s, "text returned:"); i >= 0 {
		s = s[i+len("text returned:"):]
		if j := strings.Index(s, ","); j >= 0 {
			s = s[:j]
		}
		return strings.TrimSpace(s)
	}
	return ""
}

// selectPaths 弹出系统面板，允许同时多选多个文件和文件夹；取消返回 nil
func selectPaths() []string {
	log.Println("请求弹出文件选择面板")
	p := C.SelectPathsNative()
	if p == nil {
		// 用户取消或面板未成功返回（异常）都会到这里；区分不了则记录已发生
		log.Println("文件选择面板未返回结果（可能用户取消，或面板弹出失败）")
		return nil
	}
	defer C.free(unsafe.Pointer(p))
	s := C.GoString(p)
	var paths []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		log.Println("文件选择面板返回了空结果")
		return nil
	}
	log.Printf("文件选择面板返回 %d 个路径", len(paths))
	return paths
}

// choosePeers 弹出系统多选框，让用户勾选要发送的目标设备；
// opts 形如 "设备名 (IP)"；返回选中的条目，取消返回 nil
func choosePeers(opts []string) []string {
	if len(opts) == 0 {
		return nil
	}
	var quoted []string
	for _, o := range opts {
		e := strings.ReplaceAll(o, `\`, `\\`)
		e = strings.ReplaceAll(e, `"`, `\"`)
		quoted = append(quoted, `"`+e+`"`)
	}
	script := "set devs to {" + strings.Join(quoted, ", ") + "}\n" +
		"set chosen to choose from list devs with title \"ClipSync 发送文件\" with prompt \"选择要发送的目标设备（可多选）\" with multiple selections allowed\n" +
		"if chosen is false then\nreturn \"\"\nelse\nreturn chosen\nend if"
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		log.Printf("目标设备选择框执行失败: %v", err)
		return nil
	}
	// 解析形如 {"a", "b"} 的输出，提取引号内文本
	s := string(out)
	var res []string
	start := 0
	for {
		i := strings.IndexByte(s[start:], '"')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[start+i+1:], '"')
		if j < 0 {
			break
		}
		res = append(res, s[start+i+1:start+i+1+j])
		start = start + i + 1 + j + 1
	}
	if len(res) == 0 {
		log.Println("目标设备选择框：未选择，取消")
		return nil
	}
	log.Printf("目标设备选择框返回 %d 台", len(res))
	return res
}
