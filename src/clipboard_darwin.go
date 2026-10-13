//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework Foundation
#import <AppKit/AppKit.h>

// 读剪贴板文字
static char* ReadPasteboardText() {
    NSPasteboard *pb = [NSPasteboard generalPasteboard];
    NSString *s = [pb stringForType:NSPasteboardTypeString];
    if (!s) return NULL;   // 无文字返回 NULL，Go 侧不再 free，避免 free 字面量崩溃
    return strdup([s UTF8String]);
}

// 读剪贴板图片，返回 PNG 数据
static NSData* ReadPasteboardImage() {
    NSPasteboard *pb = [NSPasteboard generalPasteboard];
    NSArray *imgs = [pb readObjectsForClasses:@[[NSImage class]] options:nil];
    if (imgs.count == 0) return nil;
    NSImage *img = imgs[0];
    NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:[img TIFFRepresentation]];
    return [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
}

static int DataLen(NSData *d) { return (int)d.length; }
static const void* DataPtr(NSData *d) { return d.bytes; }

// 写文字
static void WritePasteboardText(const char *s) {
    NSPasteboard *pb = [NSPasteboard generalPasteboard];
    [pb clearContents];
    [pb setString:[NSString stringWithUTF8String:s] forType:NSPasteboardTypeString];
}

// 写 PNG 图片
static void WritePasteboardImage(const void *data, int len) {
    NSData *nsdata = [NSData dataWithBytes:data length:len];
    NSImage *img = [[NSImage alloc] initWithData:nsdata];
    NSPasteboard *pb = [NSPasteboard generalPasteboard];
    [pb clearContents];
    [pb writeObjects:@[img]];
}

static long PasteboardChangeCount() {
    return [[NSPasteboard generalPasteboard] changeCount];
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
		if changed {
			c.lastCount = count
		}
		suppress := c.suppress
		if suppress {
			c.suppress = false
		}
		inMime := c.incomingMime
		inKey := c.incomingKey
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
		}
		if c.onChange != nil {
			c.onChange()
		}
	}
}

func (c *Clipboard) SetText(s string) {
	C.WritePasteboardText(C.CString(s))
	c.mu.Lock()
	c.lastCount = int64(C.PasteboardChangeCount())
	c.mu.Unlock()
}

func (c *Clipboard) SetImage(png []byte) {
	_ = c.SetImageSafe(png)
}

func (c *Clipboard) SetImageSafe(png []byte) error {
	if len(png) == 0 {
		return fmt.Errorf("empty png")
	}
	C.WritePasteboardImage(unsafe.Pointer(&png[0]), C.int(len(png)))
	c.mu.Lock()
	c.lastCount = int64(C.PasteboardChangeCount())
	c.mu.Unlock()
	return nil
}

// 读当前剪贴板，返回 (mimeType, data)
func (c *Clipboard) ReadCurrent() (string, []byte) {
	// 优先图片
	if imgData := C.ReadPasteboardImage(); imgData != nil {
		l := int(C.DataLen(imgData))
		buf := C.GoBytes(unsafe.Pointer(C.DataPtr(imgData)), C.int(l))
		return "image/png", buf
	}
	// 文字
	s := C.ReadPasteboardText()
	if s == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(s))
	goStr := C.GoString(s)
	if goStr == "" {
		return "", nil
	}
	return "text/plain", []byte(goStr)
}

func (c *Clipboard) SuppressNext() {
	c.mu.Lock()
	c.suppress = true
	c.mu.Unlock()
}
