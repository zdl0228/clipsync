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
	procLstrcpy                    = syscall.NewLazyDLL("kernel32.dll").NewProc("lstrcpyW")
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

func (c *Clipboard) Watch() {
	for {
		time.Sleep(500 * time.Millisecond)
		seq := getSeqNumber()

		c.mu.Lock()
		changed := seq != c.lastSeq
		if changed {
			c.lastSeq = seq
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
	procOpenClipboard.Call(0)
	procEmptyClipboard.Call()
	utf16, _ := syscall.UTF16FromString(s)
	h, _, _ := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(len(utf16)*2))
	ptr, _, _ := procGlobalLock.Call(h)
	syscall.Syscall(procLstrcpy.Addr(), 2, ptr, uintptr(unsafe.Pointer(&utf16[0])), 0)
	procGlobalUnlock.Call(h)
	procSetClipboardData.Call(CF_UNICODETEXT, h)
	procCloseClipboard.Call()
	c.mu.Lock()
	c.lastSeq = getSeqNumber()
	c.mu.Unlock()
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
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
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

func (c *Clipboard) ReadCurrent() (string, []byte) {
	// 先看有没有图片
	if r, _, _ := procIsClipboardFormatAvailable.Call(CF_DIB); r != 0 {
		procOpenClipboard.Call(0)
		hDib, _, _ := procGetClipboardData.Call(CF_DIB)
		if hDib != 0 {
			ptr, _, _ := procGlobalLock.Call(hDib)
			// 先读 header 拿尺寸
			if ptr != 0 {
				header := (*[40]byte)(unsafe.Pointer(ptr))
				w := int(int32(header[4]) | int32(header[5])<<8 | int32(header[6])<<16 | int32(header[7])<<24)
				hh := int(int32(header[8]) | int32(header[9])<<8 | int32(header[10])<<16 | int32(header[11])<<24)
				if hh < 0 {
					hh = -hh
				}
				bpp := int(header[14]) | int(header[15])<<8
				// 每行按 4 字节对齐，与 dibToPng 解码一致
				rowSize := ((w*bpp + 31) / 32) * 4
				if rowSize < 1 {
					rowSize = 1
				}
				total := 40 + rowSize*hh
				if total > 64*1024*1024 {
					total = 64 * 1024 * 1024
				}
				dib := make([]byte, total)
				copy(dib, (*[1 << 28]byte)(unsafe.Pointer(ptr))[:total])
				procGlobalUnlock.Call(hDib)
				procCloseClipboard.Call()
				pngData, err := dibToPng(dib)
				if err == nil {
					return "image/png", pngData
				}
			} else {
				procGlobalUnlock.Call(hDib)
				procCloseClipboard.Call()
			}
		} else {
			procCloseClipboard.Call()
		}
	}

	// 文字
	if r, _, _ := procIsClipboardFormatAvailable.Call(CF_UNICODETEXT); r != 0 {
		procOpenClipboard.Call(0)
		h, _, _ := procGetClipboardData.Call(CF_UNICODETEXT)
		if h != 0 {
			ptr, _, _ := procGlobalLock.Call(h)
			str := syscall.UTF16ToString((*[1 << 20]uint16)(unsafe.Pointer(ptr))[:])
			procGlobalUnlock.Call(h)
			procCloseClipboard.Call()
			return "text/plain", []byte(str)
		}
		procCloseClipboard.Call()
	}
	return "", nil
}

func (c *Clipboard) SuppressNext() {
	c.mu.Lock()
	c.suppress = true
	c.mu.Unlock()
}
