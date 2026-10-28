//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	procGlobalAlloc                = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalAlloc")
	procGlobalLock                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalLock")
	procGlobalUnlock               = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalUnlock")
	procGlobalSize                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalSize")
	procGlobalFree                 = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalFree")
)

const (
	CF_UNICODETEXT = 13
	CF_DIB         = 8
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
	h, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(utf16s)*2))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16s)), utf16s)
	procGlobalUnlock.Call(h)
	// 成功后内存块所有权交给系统，失败时自己释放，避免泄漏
	if r, _, _ := procSetClipboardData.Call(CF_UNICODETEXT, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData failed")
	}
	c.mu.Lock()
	c.lastSeq = getSeqNumber()
	c.mu.Unlock()
	return nil
}

// PNG -> DIB (BITMAPINFOHEADER + 像素, bottom-up 32bpp)
func pngToDib(pngData []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// bottom-up 32bpp DIB
	rowSize := w * 4
	totalSize := 40 + rowSize*h
	buf := make([]byte, totalSize)

	// BITMAPINFOHEADER
	// biSize
	buf[0] = 40
	// biWidth
	wr := uint32(w)
	buf[4] = byte(wr)
	buf[5] = byte(wr >> 8)
	buf[6] = byte(wr >> 16)
	buf[7] = byte(wr >> 24)
	// biHeight (正数 = bottom-up)
	hr := uint32(h)
	buf[8] = byte(hr)
	buf[9] = byte(hr >> 8)
	buf[10] = byte(hr >> 16)
	buf[11] = byte(hr >> 24)
	// biPlanes
	buf[12] = 1
	buf[13] = 0
	// biBitCount = 32
	buf[14] = 32
	buf[15] = 0
	// biCompression = 0 (BI_RGB)
	// biSizeImage
	biSize := uint32(rowSize * h)
	buf[20] = byte(biSize)
	buf[21] = byte(biSize >> 8)
	buf[22] = byte(biSize >> 16)
	buf[23] = byte(biSize >> 24)

	// 像素数据：bottom-up，从最后一行开始
	for y := 0; y < h; y++ {
		sy := h - 1 - y // bottom-up
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, sy).RGBA()
			i := 40 + (y*w+x)*4
			buf[i] = byte(b >> 8)
			buf[i+1] = byte(g >> 8)
			buf[i+2] = byte(r >> 8)
			buf[i+3] = 255
		}
	}
	return buf, nil
}

// DIB -> PNG
func dibToPng(dib []byte) ([]byte, error) {
	if len(dib) < 40 {
		return nil, fmt.Errorf("dib too short")
	}
	w := int(int32(dib[4]) | int32(dib[5])<<8 | int32(dib[6])<<16 | int32(dib[7])<<24)
	h := int(int32(dib[8]) | int32(dib[9])<<8 | int32(dib[10])<<16 | int32(dib[11])<<24)
	bpp := int(dib[14]) | int(dib[15])<<8
	topDown := h < 0
	if h < 0 {
		h = -h
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if bpp == 24 || bpp == 32 {
		bytesPerPixel := bpp / 8
		// DIB 每行按 4 字节对齐
		rowSize := ((w*bpp + 31) / 32) * 4
		for y := 0; y < h; y++ {
			sy := y
			if !topDown {
				sy = h - 1 - y
			}
			rowStart := 40 + sy*rowSize
			for x := 0; x < w; x++ {
				i := rowStart + x*bytesPerPixel
				if i+2 >= len(dib) {
					break
				}
				b, g, r := dib[i], dib[i+1], dib[i+2]
				img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (c *Clipboard) SetImage(pngData []byte) {
	_ = c.SetImageSafe(pngData)
}

func (c *Clipboard) SetImageSafe(pngData []byte) error {
	dib, err := pngToDib(pngData)
	if err != nil {
		return fmt.Errorf("pngToDib: %w", err)
	}
	log.Printf("DIB 大小: %d bytes", len(dib))

	// OpenClipboard
	if !openClipboardWithRetry() {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	h, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(dib)))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock failed")
	}
	copy((*[1 << 30]byte)(unsafe.Pointer(ptr))[:len(dib)], dib)
	procGlobalUnlock.Call(h)

	hh, _, _ := procSetClipboardData.Call(CF_DIB, h)
	log.Printf("SetClipboardData 返回: %v", hh)
	c.mu.Lock()
	c.lastSeq = getSeqNumber()
	c.mu.Unlock()
	return nil
}

// le32 读 4 字节小端有符号整数（DIB 头部字段）
func le32(b []byte) int32 {
	return int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
}

// readClipboardImagePNG 读取 CF_DIB 并转成 PNG；无图片或解析失败返回 nil。
// 所有按 header 声明尺寸去读的地方都以 GlobalSize 的实际分配大小为上限：
// 早期版本按 header 算出 total 后直接强制类型转换读取，头部被伪造时
// （如恶意网页写入畸形 DIB）会读越界崩溃。
func readClipboardImagePNG() []byte {
	if !openClipboardWithRetry() {
		return nil
	}
	defer procCloseClipboard.Call()

	hDib, _, _ := procGetClipboardData.Call(CF_DIB)
	if hDib == 0 {
		return nil
	}
	avail := globalBytes(hDib)
	if avail < 40 {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(hDib)
	if ptr == 0 {
		return nil
	}
	// 先整块快照到 Go 内存，避免持锁解析
	raw := make([]byte, avail)
	copy(raw, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), avail))
	procGlobalUnlock.Call(hDib)

	w := int(le32(raw[4:8]))
	h := int(le32(raw[8:12]))
	bpp := int(raw[14]) | int(raw[15])<<8
	topDown := h < 0
	if h < 0 {
		h = -h
	}
	rowSize := ((w*bpp + 31) / 32) * 4
	if w <= 0 || h <= 0 || rowSize <= 0 || (bpp != 24 && bpp != 32) {
		return nil
	}
	if need := 40 + rowSize*h; need > len(raw) {
		fit := (len(raw) - 40) / rowSize
		if fit <= 0 {
			return nil
		}
		// 声明尺寸超过实际分配：按实际能容纳的行数修正，并写回头部，
		// 保证 dibToPng 看到一致的尺寸而不会按谎报高度空转
		h = fit
		v := uint32(h)
		if topDown {
			v = uint32(-h)
		}
		raw[8], raw[9], raw[10], raw[11] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}

	pngData, err := dibToPng(raw)
	if err != nil {
		log.Printf("DIB 转 PNG 失败: %v", err)
		return nil
	}
	return pngData
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
	buf := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), avail/2)
	// 必须在 Unlock 前完成拷贝，Unlock 后指针不再有效
	s := utf16ToStringBounded(buf)
	procGlobalUnlock.Call(h)
	return s, true
}

func (c *Clipboard) ReadCurrent() (string, []byte) {
	// 先看有没有图片
	if r, _, _ := procIsClipboardFormatAvailable.Call(CF_DIB); r != 0 {
		if pngData := readClipboardImagePNG(); pngData != nil {
			return "image/png", pngData
		}
	}
	// 文字
	if r, _, _ := procIsClipboardFormatAvailable.Call(CF_UNICODETEXT); r != 0 {
		if s, ok := readClipboardText(); ok && s != "" {
			return "text/plain", []byte(s)
		}
	}
	return "", nil
}

func (c *Clipboard) SuppressNext() {
	c.mu.Lock()
	c.suppress = true
	c.mu.Unlock()
}
