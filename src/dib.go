package main

// DIB（Windows 设备无关位图）与 PNG 之间的互转。
//
// 放在无 build tag 的独立文件里，是为了让 dib_test.go 能在 macOS 上跑：
// 剪贴板 syscall 部分只存在于 clipboard_windows.go，但像素格式转换是纯计算，
// 恰恰也是最容易出错、最需要测试锁住的部分（alpha 通道就是其中之一）。
//
// 关于透明通道，Windows 上有三套并存的"事实标准"，这里都要照顾到：
//   1. CF_DIB (v3, 40 字节头) + 32bpp + BI_RGB：第 4 个字节被约定为 alpha，
//      但 GDI 画图之类的老程序会直接忽略它（按不透明显示），所以填真实 alpha 是安全的。
//   2. CF_DIBV5 (124 字节头) + BI_BITFIELDS + bV5AlphaMask=0xFF000000：唯一
//      有明确规范声明 alpha 的格式，Photoshop / WIC / 照片查看器走这条。
//   3. 注册格式 "PNG"：Chrome/Edge/Firefox 走这条，无损且自带 alpha，最省事。
// 旧实现两头都没处理：写的时候硬写 alpha=255，读的时候硬给 A=255，
// 于是带透明通道的图穿过 Windows 就一定变成不透明。本文件把三套都补上。

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
)

const (
	dibHeaderSizeV3 = 40  // BITMAPINFOHEADER
	dibHeaderSizeV5 = 124 // BITMAPV5HEADER

	biRGB       = 0 // BI_RGB
	biBitFields = 3 // BI_BITFIELDS

	lcsSRGB = 0x73524742 // 'sRGB'
)

// dibInfo 是解析出来的 DIB 头部信息
type dibInfo struct {
	Width       int
	Height      int // 已取绝对值
	TopDown     bool
	BPP         int
	HeaderSize  int
	Compression uint32
	AlphaMask   uint32
	PixelOffset int // 像素数据起点（含 masks / 调色板偏移）
	RowSize     int // 每行字节数（4 字节对齐）
}

func (d dibInfo) bytesPerPixel() int { return d.BPP / 8 }

// le32u 读 4 字节小端无符号整数
func le32u(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// le32 读 4 字节小端有符号整数（DIB 头部字段）
func le32(b []byte) int32 { return int32(le32u(b)) }

func putLE16(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

func putLE32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

// parseDib 解析头部并算好像素布局。
//
// 所有尺寸都对照 len(b) 校验过再裁剪：剪贴板里的头部可能来自恶意网页，
// 谎报 width/height 会让算出的行偏移落到分配范围之外。旧实现边算边读，
// 头部一被伪造就 slice 越界 panic。
func parseDib(b []byte) (dibInfo, error) {
	var d dibInfo
	if len(b) < dibHeaderSizeV3 {
		return d, fmt.Errorf("dib too short: %d", len(b))
	}
	d.HeaderSize = int(le32(b[0:4]))
	// 支持的只有 40（v3）、108（v4）、124（v5）。BITMAPCOREHEADER(12)
	// 是 OS/2 格式，字段宽度都不一样，剪贴板里基本不会出现，直接拒绝。
	if d.HeaderSize < dibHeaderSizeV3 || d.HeaderSize > len(b) {
		return d, fmt.Errorf("bad dib header size: %d", d.HeaderSize)
	}

	d.Width = int(le32(b[4:8]))
	rawH := int(le32(b[8:12]))
	d.TopDown = rawH < 0
	d.Height = rawH
	if d.TopDown {
		d.Height = -rawH
	}
	d.BPP = int(b[14]) | int(b[15])<<8
	d.Compression = le32u(b[16:20])

	// 每边 20 万像素已经是 40GB 量级，先挡掉明显离谱的值再往下算
	if d.Width <= 0 || d.Height <= 0 || d.Width > 200000 || d.Height > 200000 {
		return d, fmt.Errorf("bad dib size: %dx%d", d.Width, d.Height)
	}
	if d.BPP != 24 && d.BPP != 32 {
		return d, fmt.Errorf("unsupported bpp: %d", d.BPP)
	}

	// 像素起点：头部之后可能跟 masks（仅 40 字节头 + BI_BITFIELDS 这种老写法）
	offset := d.HeaderSize
	if d.Compression == biBitFields && d.HeaderSize == dibHeaderSizeV3 {
		if len(b) < offset+12 {
			return d, fmt.Errorf("truncated bitfields masks")
		}
		offset += 12
	}
	// v4/v5 的 masks 在头部内部，alphaMask 位于偏移 52
	if d.HeaderSize >= 56 {
		d.AlphaMask = le32u(b[52:56])
	}
	// 24/32bpp 理论上 ClrUsed=0；万一有调色板也要跳过，否则整张图错位
	if clrUsed := le32u(b[32:36]); clrUsed > 0 && d.HeaderSize >= dibHeaderSizeV3 {
		if clrUsed > 256 {
			return d, fmt.Errorf("bad clrUsed: %d", clrUsed)
		}
		offset += int(clrUsed) * 4
	}
	d.PixelOffset = offset

	d.RowSize = ((d.Width*d.BPP + 31) / 32) * 4
	if d.RowSize <= 0 || d.PixelOffset+d.RowSize > len(b) {
		return d, fmt.Errorf("dib body truncated: need %d, have %d", d.PixelOffset+d.RowSize, len(b))
	}

	// 声明高度超过实际能容纳的行数时，按实际裁剪——有些程序会在 DIB 后面
	// 追加自己的私有数据，或者干脆把 biSizeImage 报大。
	if fit := (len(b) - d.PixelOffset) / d.RowSize; fit < d.Height {
		d.Height = fit
		if d.Height <= 0 {
			return d, fmt.Errorf("dib has no complete row")
		}
	}
	return d, nil
}

// usableAlpha 判断 32bpp DIB 的第 4 个字节能不能当 alpha 用。
//
// 这里要区分两种"alpha 全是 0"的情况，否则会从一个极端掉到另一个极端：
//
//	A. 头部声明了 bV5AlphaMask = 0xFF000000（CF_DIBV5 的标准写法）：
//	   写字的一方是 alpha-aware 的，全 0 就是真的全透明，照用。
//	B. 40 字节头 BI_RGB 的"事实约定"：GDI 抓屏 / 截图工具压根不填这一列，
//	   整片是 0。照用会让整张图消失，只能按不透明理解。
//	   行业惯例（WIC、GDI+、Qt 一致）：全 0 视为不透明。
//
// 其余 mask 值说明第 4 字节另有用途（某些 BI_BITFIELDS 布局下的填充），直接不用。
func usableAlpha(dib []byte, d dibInfo) bool {
	if d.BPP != 32 {
		return false
	}
	if d.AlphaMask == 0xFF000000 {
		return true
	}
	if d.AlphaMask != 0 {
		return false
	}
	step := d.bytesPerPixel()
	for y := 0; y < d.Height; y++ {
		row := dib[d.PixelOffset+y*d.RowSize:]
		for x := 0; x < d.Width; x++ {
			if row[x*step+3] != 0 {
				return true
			}
		}
	}
	return false
}

// dibToPng 把 DIB 解码成 PNG。
func dibToPng(dib []byte) ([]byte, error) {
	d, err := parseDib(dib)
	if err != nil {
		return nil, err
	}
	alpha := usableAlpha(dib, d)

	w, h := d.Width, d.Height
	// 用 NRGBA 而不是 RGBA：DIB 里的 alpha 是非预乘的，RGBA 语义是预乘，
	// 混用会让半透明像素的颜色整体偏暗；而且 png.Encode 对 NRGBA 会做
	// Opaque() 检查，整张不透明时自动输出 RGB PNG，不白占一条 alpha 通道。
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	step := d.bytesPerPixel()

	for y := 0; y < h; y++ {
		srcY := y
		if !d.TopDown {
			srcY = h - 1 - y
		}
		row := dib[d.PixelOffset+srcY*d.RowSize:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			si := x * step
			di := x * 4
			dst[di+0] = row[si+2] // R
			dst[di+1] = row[si+1] // G
			dst[di+2] = row[si+0] // B
			if alpha {
				dst[di+3] = row[si+3]
			} else {
				dst[di+3] = 255
			}
		}
	}

	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decodePngToNRGBA 统一转换到 NRGBA，避免后续处处处理各种 ColorModel。
func decodePngToNRGBA(pngData []byte) (*image.NRGBA, error) {
	src, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	if n, ok := src.(*image.NRGBA); ok && b.Min == (image.Point{}) {
		return n, nil
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst, nil
}

// hasTransparency 判断是否存在真正的半透明/全透明像素。
// 全不透明时没必要写 CF_DIBV5，少一种格式就少一分兼容风险。
func hasTransparency(img *image.NRGBA) bool { return !img.Opaque() }

// nrgbaToDibV3 生成 40 字节头的 CF_DIB（32bpp BI_RGB，bottom-up）。
// alpha 写真实值：GDI 读的时候会忽略这一列按不透明画，WIC/Chrome 读的时候
// 会用它——两边都不会出问题。
func nrgbaToDibV3(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	rowSize := w * 4
	buf := make([]byte, dibHeaderSizeV3+rowSize*h)

	putLE32(buf[0:], dibHeaderSizeV3)
	putLE32(buf[4:], uint32(w))
	putLE32(buf[8:], uint32(h)) // 正数 = bottom-up
	putLE16(buf[12:], 1)        // biPlanes
	putLE16(buf[14:], 32)       // biBitCount
	putLE32(buf[16:], biRGB)
	putLE32(buf[20:], uint32(rowSize*h)) // biSizeImage
	putLE32(buf[24:], 2835)              // 72 DPI
	putLE32(buf[28:], 2835)

	for y := 0; y < h; y++ {
		src := img.Pix[(h-1-y)*img.Stride:]
		dst := buf[dibHeaderSizeV3+y*rowSize:]
		for x := 0; x < w; x++ {
			si, di := x*4, x*4
			dst[di+0] = src[si+2] // B
			dst[di+1] = src[si+1] // G
			dst[di+2] = src[si+0] // R
			dst[di+3] = src[si+3] // A
		}
	}
	return buf
}

// nrgbaToDibV5 生成 124 字节头的 CF_DIBV5（BI_BITFIELDS + alpha mask）。
// 这是唯一在规范层面声明了 alpha 的格式，Photoshop / 照片查看器只认它。
func nrgbaToDibV5(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	rowSize := w * 4
	buf := make([]byte, dibHeaderSizeV5+rowSize*h)

	putLE32(buf[0:], dibHeaderSizeV5)
	putLE32(buf[4:], uint32(w))
	putLE32(buf[8:], uint32(h))
	putLE16(buf[12:], 1)
	putLE16(buf[14:], 32)
	putLE32(buf[16:], biBitFields)
	putLE32(buf[20:], uint32(rowSize*h))
	putLE32(buf[24:], 2835)
	putLE32(buf[28:], 2835)
	// 32: bV5ClrUsed / 36: bV5ClrImportant 保持 0
	putLE32(buf[40:], 0x00FF0000) // R mask
	putLE32(buf[44:], 0x0000FF00) // G mask
	putLE32(buf[48:], 0x000000FF) // B mask
	putLE32(buf[52:], 0xFF000000) // A mask
	putLE32(buf[56:], lcsSRGB)    // bV5CSType
	// 60..96: bV5Endpoints 留 0（sRGB 时被忽略）
	putLE32(buf[108:], 4) // bV5Intent = LCS_GM_IMAGES

	for y := 0; y < h; y++ {
		src := img.Pix[(h-1-y)*img.Stride:]
		dst := buf[dibHeaderSizeV5+y*rowSize:]
		for x := 0; x < w; x++ {
			si, di := x*4, x*4
			dst[di+0] = src[si+2]
			dst[di+1] = src[si+1]
			dst[di+2] = src[si+0]
			dst[di+3] = src[si+3]
		}
	}
	return buf
}

// isPNG 判断一段字节是否以 PNG 签名开头
func isPNG(b []byte) bool {
	return len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G'
}
