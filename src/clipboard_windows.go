//go:build windows

package main

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc                = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalAlloc")
	procGlobalLock                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalLock")
	procGlobalUnlock               = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalUnlock")
	procGlobalSize                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalSize")
	procGlobalFree                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalFree")
)

const (
	CF_UNICODETEXT = 13
	CF_DIB         = 8
	CF_DIBV5       = 17
	GMEM_MOVEABLE  = 0x0002
)

type Clipboard struct {
	mu           sync.Mutex // 保护以下字段（Watch 与 readLoop 多 goroutine 访问）
	lastSeq      uint32
	suppress     bool
	incomingMime string
	incomingKey  string
	onChange     func()
}

func NewClipboard() *Clipboard {
	return &Clipboard{lastSeq: getSeqNumber()}
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

func getSeqNumber() uint32 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(r)
}

// openClipboardWithRetry 打开剪贴板；其它程序（输入法、Office、截图工具等）
// 会短暂独占剪贴板，OpenClipboard 立即返回失败，所以重试几次而不是直接放弃。
func openClipboardWithRetry() bool {
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// globalBytes 返回剪贴板内存块的实际字节数。
// 用它给所有"按 header 声明的尺寸去读"的地方封顶，避免头部字段被伪造
// （例如恶意网页写入的畸形 DIB）时读出分配范围之外、直接崩溃。
func globalBytes(h uintptr) int {
	n, _, _ := procGlobalSize.Call(h)
	if n == 0 || n > 256*1024*1024 {
		return 0
	}
	return int(n)
}

// pngClipboardFormat 返回注册格式 "PNG" 的格式 ID（0 表示注册失败）。
// Chrome / Edge / Firefox / 微信 在写图片剪贴板时都会带这个格式，读它比读
// DIB 更保真：无损、带 alpha、不用猜第 4 个字节到底是什么。
var (
	pngFmtOnce sync.Once
	pngFmtID   uint32
)

func pngClipboardFormat() uint32 {
	pngFmtOnce.Do(func() {
		name, err := syscall.UTF16PtrFromString("PNG")
		if err != nil {
			return
		}
		r, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
		pngFmtID = uint32(r)
	})
	return pngFmtID
}

// globalSlice 把 GlobalLock 返回的地址包装成切片。
//
// vet 会在这里报 possible misuse of unsafe.Pointer：它担心的是 Go 堆对象被 GC
// 搬走后地址失效。这里不适用——内存块由 HGLOBAL 钉住、不归 Go GC 管，地址在
// GlobalUnlock 之前一直有效。把转换收敛到这一个函数，是为了"为什么安全"只
// 解释一次；调用方仍需保证：先判 ptr != 0，且 Unlock 之后不再使用切片。
func globalSlice[T any](addr uintptr, n int) []T {
	return unsafe.Slice((*T)(unsafe.Pointer(addr)), n)
}

// setClipboardBlob 把一段字节放进剪贴板的指定格式。
// 调用前必须已经 OpenClipboard + EmptyClipboard。
// 成功后内存块所有权交给系统，失败必须自己释放，否则每次传图都漏一块。
func setClipboardBlob(format uint32, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty blob")
	}
	h, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(data)))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(globalSlice[byte](ptr, len(data)), data)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(uintptr(format), h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData(%d) failed", format)
	}
	return nil
}

// readClipboardFormatBytes 把指定格式的数据整体快照到 Go 内存，调用前须已打开剪贴板。
// 先快照再解析：解析过程不持有全局锁，也不会因为持有 HGLOBAL 锁而卡住其它程序。
func readClipboardFormatBytes(format uint32) []byte {
	h, _, _ := procGetClipboardData.Call(uintptr(format))
	if h == 0 {
		return nil
	}
	avail := globalBytes(h)
	if avail <= 0 {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil
	}
	raw := make([]byte, avail)
	copy(raw, globalSlice[byte](ptr, avail))
	procGlobalUnlock.Call(h)
	return raw
}

// utf16FromStringSafe 生成以 0 结尾的 UTF-16 序列，替代 syscall.UTF16FromString。
// 后者遇到含 NUL 的字符串会返回 error 和 nil 切片，原代码忽略 error 后
// 紧接着 &utf16[0] 会直接越界 panic（等于一条含 \x00 的文本就能踢掉连接）。
// 这里把 NUL 换成空格，并保证非空。
func utf16FromStringSafe(s string) []uint16 {
	rs := []rune(s)
	for i, r := range rs {
		if r == 0 {
			rs[i] = ' '
		}
	}
	return append(utf16.Encode(rs), 0)
}

// utf16ToStringBounded 在给定范围内找 NUL 并解码，绝不越界读
func utf16ToStringBounded(b []uint16) string {
	for i, v := range b {
		if v == 0 {
			return string(utf16.Decode(b[:i]))
		}
	}
	return string(utf16.Decode(b))
}

func (c *Clipboard) Watch() {
	for {
		time.Sleep(500 * time.Millisecond)
		seq := getSeqNumber()

		c.mu.Lock()
		changed := seq != c.lastSeq
		// 抑制标志只在"确实检测到变化"时消费：早期版本无论 changed 与否都清掉
		// suppress，若轮询正好落在 NoteIncoming 与 SetText 之间，标志被空耗，
		// 紧随其后的真实变更就会被当成本地内容再发回去（回环）。
		suppress := false
		var inMime, inKey string
		if changed {
			c.lastSeq = seq
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
	if !openClipboardWithRetry() {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()
	utf16s := utf16FromStringSafe(s)
	if err := setClipboardBlob(CF_UNICODETEXT, unsafe.Slice((*byte)(unsafe.Pointer(&utf16s[0])), len(utf16s)*2)); err != nil {
		return err
	}
	c.mu.Lock()
	c.lastSeq = getSeqNumber()
	c.mu.Unlock()
	return nil
}

// SetImageSafe 把 PNG 写进剪贴板。
//
// 同时写三种格式，覆盖不同类型的接收方（少写一种就有一类程序读不到 alpha）：
//
//	CF_DIB   40 字节头 32bpp：画图、老版 Office 这类只认 v3 的程序靠它；
//	CF_DIBV5 124 字节头 + alpha mask：Photoshop、照片查看器靠它；
//	"PNG"    注册格式：浏览器、微信、QQ 靠它（无损）。
//
// 图像完全不透明时不写 CF_DIBV5：少一种格式就少一分被老程序误读的机会。
func (c *Clipboard) SetImageSafe(pngData []byte) error {
	img, err := decodePngToNRGBA(pngData)
	if err != nil {
		return fmt.Errorf("decode png: %w", err)
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w == 0 || h == 0 || w > 200000 || h > 200000 {
		return fmt.Errorf("bad image size: %dx%d", w, h)
	}
	transparent := hasTransparency(img)

	if !openClipboardWithRetry() {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard failed")
	}

	// CF_DIB 是必选项，失败就整体失败
	if err := setClipboardBlob(CF_DIB, nrgbaToDibV3(img)); err != nil {
		return fmt.Errorf("写入 CF_DIB 失败: %w", err)
	}
	// 下面两种是增强项，失败只记日志——CF_DIB 已经写进去了，图能看
	if transparent {
		if err := setClipboardBlob(CF_DIBV5, nrgbaToDibV5(img)); err != nil {
			log.Printf("写入 CF_DIBV5 失败（已降级为 CF_DIB，透明通道可能丢失）: %v", err)
		}
	}
	if f := pngClipboardFormat(); f != 0 {
		if err := setClipboardBlob(f, pngData); err != nil {
			log.Printf("写入 PNG 剪贴板格式失败: %v", err)
		}
	}

	c.mu.Lock()
	c.lastSeq = getSeqNumber()
	c.mu.Unlock()
	log.Printf("图片已写入剪贴板: %dx%d, 带透明=%v", w, h, transparent)
	return nil
}

// clipboardHasImage 判断剪贴板里是否挂着图片（任一可用格式）
func clipboardHasImage() bool {
	formats := []uint32{CF_DIB, CF_DIBV5}
	if f := pngClipboardFormat(); f != 0 {
		formats = append(formats, f)
	}
	for _, f := range formats {
		if r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(f)); r != 0 {
			return true
		}
	}
	return false
}

// readClipboardImagePNG 读剪贴板图片并转成 PNG；无图片或解析失败返回 nil。
//
// 按保真度从高到低尝试：PNG → CF_DIBV5 → CF_DIB。
// 旧实现只读 CF_DIB 且硬给 A=255，这是"带透明通道的图变不透明"的直接原因。
func readClipboardImagePNG() []byte {
	if !openClipboardWithRetry() {
		return nil
	}
	defer procCloseClipboard.Call()

	// 1) 注册格式 PNG：原样带走，一个像素都不用重新解释
	if f := pngClipboardFormat(); f != 0 {
		if raw := readClipboardFormatBytes(f); isPNG(raw) {
			return raw
		}
	}
	// 2) CF_DIBV5 → 3) CF_DIB，失败则降级到下一个
	for _, f := range []uint32{CF_DIBV5, CF_DIB} {
		raw := readClipboardFormatBytes(f)
		if len(raw) < dibHeaderSizeV3 {
			continue
		}
		pngData, err := dibToPng(raw)
		if err != nil {
			log.Printf("DIB 格式 %d 转 PNG 失败: %v", f, err)
			continue
		}
		return pngData
	}
	return nil
}

// readClipboardText 读取 CF_UNICODETEXT；第二个返回值表示是否成功取到
func readClipboardText() (string, bool) {
	if !openClipboardWithRetry() {
		return "", false
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(CF_UNICODETEXT)
	if h == 0 {
		return "", false
	}
	avail := globalBytes(h)
	if avail < 2 {
		return "", false
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", false
	}
	// 读取长度以实际分配为准，再在其中找 NUL 截止；
	// 原实现用 (*[1<<20]uint16)[:] 硬读 2MB，块小于该值时同样会越界
	buf := globalSlice[uint16](ptr, avail/2)
	// 必须在 Unlock 前完成拷贝，Unlock 后指针不再有效
	s := utf16ToStringBounded(buf)
	procGlobalUnlock.Call(h)
	return s, true
}

// ReadCurrent 读当前剪贴板内容。
//
// **先文字、后图片**：Excel / Word / 浏览器复制单元格或富文本时，剪贴板里
// 同时挂着 CF_UNICODETEXT 和 CF_DIB（后者只是这段内容的视觉快照）。
// 原实现先取图片，于是"复制几个字"，对端却收到一张截图 —— 逻辑上是错的。
// 真正只有图片的场景（截图工具、复制图像）没有 CF_UNICODETEXT，仍然走图片分支。
func (c *Clipboard) ReadCurrent() (string, []byte) {
	if r, _, _ := procIsClipboardFormatAvailable.Call(CF_UNICODETEXT); r != 0 {
		if s, ok := readClipboardText(); ok && s != "" {
			return "text/plain", []byte(s)
		}
	}
	if clipboardHasImage() {
		if pngData := readClipboardImagePNG(); pngData != nil {
			return "image/png", pngData
		}
	}
	return "", nil
}
