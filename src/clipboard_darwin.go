//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework Foundation
#import <AppKit/AppKit.h>
#import <stdlib.h>
#import <string.h>

// 注意：本文件所有 C 函数都在 Go 的普通 goroutine（Watch / readLoop）里被调用，
// 这些线程没有 Cocoa 主循环的 autorelease pool，因此每个函数都用
// @autoreleasepool 显式包起来，并把结果复制成 malloc 缓冲返回由 Go free。
// 早期版本直接把 NSData* 交给 Go、用 C.CString 传文字且不 free，
// 导致每次剪贴板变化都泄漏一批 autoreleased 对象和一个 C 字符串。

// 读剪贴板文字：返回 malloc 的 UTF-8 缓冲（调用方 free），*outLen 为字节数；
// 无文字时返回 NULL。用 dataUsingEncoding 而不是 UTF8String，可正确处理含
// NUL / 孤立代理项的字符串，并给出准确字节长度。
static char* ReadPasteboardText(int *outLen) {
    *outLen = 0;
    char *result = NULL;
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSString *s = [pb stringForType:NSPasteboardTypeString];
        if (!s) return NULL;
        NSData *d = [s dataUsingEncoding:NSUTF8StringEncoding];
        if (!d) return NULL;
        NSUInteger n = d.length;
        if (n == 0) return NULL;
        result = (char*)malloc(n);
        if (result) {
            memcpy(result, d.bytes, n);
            *outLen = (int)n;
        }
    }
    return result;
}

// 读剪贴板图片，转成 PNG 并复制到 malloc 缓冲（调用方 free）；无图片返回 NULL
static void* ReadPasteboardImagePNG(int *outLen) {
    *outLen = 0;
    void *result = NULL;
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSArray *imgs = [pb readObjectsForClasses:@[[NSImage class]] options:nil];
        if (imgs.count == 0) return NULL;
        NSImage *img = imgs[0];
        NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:[img TIFFRepresentation]];
        if (!rep) return NULL;
        NSData *d = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
        if (!d || d.length == 0) return NULL;
        NSUInteger n = d.length;
        result = malloc(n);
        if (result) {
            memcpy(result, d.bytes, n);
            *outLen = (int)n;
        }
    }
    return result;
}

// 写文字：直接按 UTF-8 字节写入（含 NUL 也不截断）；返回 1 成功 / 0 失败
static int WritePasteboardText(const char *utf8, int len) {
    @autoreleasepool {
        if (len < 0) return 0;
        const char *src = utf8 ? utf8 : "";
        NSString *s = [[NSString alloc] initWithBytes:src length:(NSUInteger)len encoding:NSUTF8StringEncoding];
        if (!s) {
            // 非 UTF-8 时按 Latin-1 兜底，避免整条内容丢失
            s = [[NSString alloc] initWithBytes:src length:(NSUInteger)len encoding:NSISOLatin1StringEncoding];
        }
        if (!s) return 0;
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        BOOL ok = [pb setString:s forType:NSPasteboardTypeString];
        [s release];
        return ok ? 1 : 0;
    }
}

// 写 PNG 图片；返回 1 成功 / 0 失败
static int WritePasteboardImage(const void *data, int len) {
    @autoreleasepool {
        if (!data || len <= 0) return 0;
        NSData *nsdata = [NSData dataWithBytes:data length:(NSUInteger)len];
        NSImage *img = [[NSImage alloc] initWithData:nsdata];
        if (!img) return 0;
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        BOOL ok = [pb writeObjects:@[img]];
        [img release];
        return ok ? 1 : 0;
    }
}

static long PasteboardChangeCount() {
    @autoreleasepool {
        return [[NSPasteboard generalPasteboard] changeCount];
    }
}
*/
import "C"
import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

type Clipboard struct {
	mu           sync.Mutex // 保护以下字段（Watch 与 readLoop 多 goroutine 访问）
	lastCount    int64
	suppress     bool
	incomingMime string
	incomingKey  string
	onChange     func()
}

func NewClipboard() *Clipboard {
	return &Clipboard{lastCount: int64(C.PasteboardChangeCount())}
}

func (c *Clipboard) OnChange(f func()) { c.onChange = f }

// NoteIncoming 记录刚收到并写入剪贴板的内容，用于防回环
func (c *Clipboard) NoteIncoming(mime string, body []byte) {
	c.mu.Lock()
	c.suppress = true
	c.incomingMime = mime
	c.incomingKey = fingerprint(body)
	c.mu.Unlock()
}

func (c *Clipboard) Watch() {
	for {
		time.Sleep(500 * time.Millisecond)
		count := int64(C.PasteboardChangeCount())

		c.mu.Lock()
		changed := count != c.lastCount
		// 抑制标志只在"确实检测到变化"时消费。
		// 早期版本无论 changed 与否都清 suppress，若轮询正好落在
		// NoteIncoming 与 SetText 之间（内容尚未写入），标志被空耗，
		// 紧随其后的真实变更就会被当成本地内容再发回去（回环）。
		suppress := false
		var inMime, inKey string
		if changed {
			c.lastCount = count
			if c.suppress {
				suppress = true
				c.suppress = false
				inMime, inKey = c.incomingMime, c.incomingKey
			}
		}
		c.mu.Unlock()

		if !changed {
			continue
		}
		// 与刚收到的一致则视为回环，跳过；否则当作本地新变化
		if suppress {
			mime, body := c.ReadCurrent()
			if mime == inMime && fingerprint(body) == inKey {
				continue
			}
			if body == nil {
				// 刚 NoteIncoming、内容还没真正落到剪贴板时读不到东西。
				// 这时把抑制标志放回去，下一轮再比一次；否则会把自己刚写进去的
				// 内容当成"本地新变化"发回去（一次多余的双向回环）。
				c.mu.Lock()
				c.suppress = true
				c.mu.Unlock()
				continue
			}
		}
		if c.onChange != nil {
			c.onChange()
		}
	}
}

func (c *Clipboard) SetText(s string) error {
	var p unsafe.Pointer
	if len(s) > 0 {
		p = unsafe.Pointer(unsafe.StringData(s))
	}
	if C.WritePasteboardText((*C.char)(p), C.int(len(s))) == 0 {
		return fmt.Errorf("写入剪贴板文字失败")
	}
	c.mu.Lock()
	c.lastCount = int64(C.PasteboardChangeCount())
	c.mu.Unlock()
	return nil
}

func (c *Clipboard) SetImageSafe(png []byte) error {
	if len(png) == 0 {
		return fmt.Errorf("empty png")
	}
	if C.WritePasteboardImage(unsafe.Pointer(&png[0]), C.int(len(png))) == 0 {
		return fmt.Errorf("写入剪贴板图片失败")
	}
	c.mu.Lock()
	c.lastCount = int64(C.PasteboardChangeCount())
	c.mu.Unlock()
	return nil
}

// 读当前剪贴板，返回 (mimeType, data)
//
// **先文字、后图片**：Excel / Word / 浏览器复制单元格或富文本时，剪贴板里
// 同时有 public.utf8-plain-text 和 public.tiff（后者只是这段内容的视觉快照）。
// 原实现优先图片，于是"复制几个字"对端收到一张截图 —— 逻辑上是错的。
// 真正只有图片的场景（截图工具、右键复制图像）没有纯文本类型，仍走图片分支。
func (c *Clipboard) ReadCurrent() (string, []byte) {
	var n C.int
	if p := C.ReadPasteboardText(&n); p != nil {
		defer C.free(unsafe.Pointer(p))
		return "text/plain", C.GoBytes(unsafe.Pointer(p), n)
	}
	n = 0
	if p := C.ReadPasteboardImagePNG(&n); p != nil {
		defer C.free(p)
		return "image/png", C.GoBytes(p, n)
	}
	return "", nil
}
