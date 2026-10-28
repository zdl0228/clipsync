package main

// DIB/PNG 互转的回归测试。
// 这些用例盯的是同一个历史 bug：PNG 带 alpha 穿过 Windows 剪贴板后变成不透明。
// 根因有两处——写的时候硬写 255、读的时候硬给 255，这里两头都锁住。

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

func testImageWithAlpha() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})       // 不透明红
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, A: 128})       // 半透明红
	img.SetNRGBA(2, 0, color.NRGBA{G: 255, A: 0})         // 全透明
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})       // 不透明蓝
	img.SetNRGBA(1, 1, color.NRGBA{B: 128, A: 64})        // 低不透明蓝
	img.SetNRGBA(2, 1, color.NRGBA{G: 10, B: 20, A: 200}) // 随机半透明
	return img
}

func encodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return b.Bytes()
}

func decodeTestPNG(t *testing.T, data []byte) *image.NRGBA {
	t.Helper()
	m, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	b := m.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), m, b.Min, draw.Src)
	return out
}

// assertSamePixels 逐像素比较 NRGBA 分量
func assertSamePixels(t *testing.T, want, got *image.NRGBA) {
	t.Helper()
	if want.Rect != got.Rect {
		t.Fatalf("尺寸不一致: want %v, got %v", want.Rect, got.Rect)
	}
	for y := 0; y < want.Rect.Dy(); y++ {
		for x := 0; x < want.Rect.Dx(); x++ {
			w := want.NRGBAAt(x, y)
			g := got.NRGBAAt(x, y)
			if w != g {
				t.Fatalf("像素 (%d,%d) 不一致: want %+v, got %+v", x, y, w, g)
			}
		}
	}
}

// ---- 写入方向：PNG → DIB ----

func TestDibV3KeepsAlpha(t *testing.T) {
	src := testImageWithAlpha()
	img, err := decodePngToNRGBA(encodeTestPNG(t, src))
	if err != nil {
		t.Fatalf("decodePngToNRGBA: %v", err)
	}
	dib := nrgbaToDibV3(img)

	// 32bpp bottom-up：底行第一个像素是 (0,1)，蓝色不透明 → BGRA = 255,0,0,255
	i := dibHeaderSizeV3
	if dib[i] != 255 || dib[i+1] != 0 || dib[i+2] != 0 || dib[i+3] != 255 {
		t.Fatalf("首像素 BGRA 错误: %v", dib[i:i+4])
	}

	back, err := dibToPng(dib)
	if err != nil {
		t.Fatalf("dibToPng: %v", err)
	}
	assertSamePixels(t, img, decodeTestPNG(t, back))
}

func TestDibV5KeepsAlpha(t *testing.T) {
	src := testImageWithAlpha()
	img, err := decodePngToNRGBA(encodeTestPNG(t, src))
	if err != nil {
		t.Fatalf("decodePngToNRGBA: %v", err)
	}
	dib := nrgbaToDibV5(img)

	if got := le32u(dib[0:4]); got != dibHeaderSizeV5 {
		t.Fatalf("biSize = %d, want %d", got, dibHeaderSizeV5)
	}
	if got := le32u(dib[16:20]); got != biBitFields {
		t.Fatalf("biCompression = %d, want BI_BITFIELDS", got)
	}
	maskChecks := []struct {
		name string
		off  int
		want uint32
	}{
		{"alpha", 52, 0xFF000000},
		{"red", 40, 0x00FF0000},
		{"green", 44, 0x0000FF00},
		{"blue", 48, 0x000000FF},
		{"cstype", 56, lcsSRGB},
	}
	for _, c := range maskChecks {
		if got := le32u(dib[c.off : c.off+4]); got != c.want {
			t.Fatalf("%s mask = %#x, want %#x", c.name, got, c.want)
		}
	}

	back, err := dibToPng(dib)
	if err != nil {
		t.Fatalf("dibToPng: %v", err)
	}
	got := decodeTestPNG(t, back)
	assertSamePixels(t, img, got)
	// 半透明像素必须还是半透明——这正是历史上丢掉的东西
	if a := got.NRGBAAt(1, 0).A; a != 128 {
		t.Fatalf("半透明像素 alpha = %d, want 128", a)
	}
}

func TestDibV5RoundTripAlphaZeroPixel(t *testing.T) {
	// 全透明像素的颜色分量也要原样保留（PNG 里 alhpa=0 的像素 RGB 是有意义的，
	// 用于正确的双线性插值），不能被"按 alpha 预乘"抹成 0
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 0})
	img, err := decodePngToNRGBA(encodeTestPNG(t, src))
	if err != nil {
		t.Fatal(err)
	}
	back, err := dibToPng(nrgbaToDibV5(img))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestPNG(t, back).NRGBAAt(0, 0)
	if got != (color.NRGBA{R: 200, G: 100, B: 50, A: 0}) {
		t.Fatalf("全透明像素被改写: %+v", got)
	}
}

func TestOpaqueImageHasNoTransparency(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := range src.Pix {
		src.Pix[i] = 200
	}
	for i := 3; i < len(src.Pix); i += 4 {
		src.Pix[i] = 255
	}
	if hasTransparency(src) {
		t.Fatal("全不透明图被判为带透明")
	}
	src.SetNRGBA(1, 1, color.NRGBA{A: 254})
	if !hasTransparency(src) {
		t.Fatal("存在 alpha<255 的像素却没被判为带透明")
	}
}

func TestOpaqueImageRoundTrip(t *testing.T) {
	// 不透明图（普通截图、照片）走的是"不写 CF_DIBV5"的那条路径。
	// 这条路径必须和改 alpha 之前完全等价——像素一个不差、alpha 全 255，
	// 而且不能被"全 0 alpha 视为不透明"的启发式误伤到（它本来就不该介入）。
	src := image.NewNRGBA(image.Rect(0, 0, 5, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			src.SetNRGBA(x, y, color.NRGBA{
				R: uint8(20 + x*45), G: uint8(30 + y*80), B: uint8(10 + x*y*20), A: 255,
			})
		}
	}
	if hasTransparency(src) {
		t.Fatal("不透明图被判为带透明，会白写一份 CF_DIBV5")
	}

	dib := nrgbaToDibV3(src)
	// 头部必须是老格式：40 字节头 + BI_RGB，兼容性最好
	if got := le32u(dib[0:4]); got != dibHeaderSizeV3 {
		t.Fatalf("biSize = %d, want %d", got, dibHeaderSizeV3)
	}
	if got := le32u(dib[16:20]); got != biRGB {
		t.Fatalf("biCompression = %d, want BI_RGB", got)
	}
	// alpha 字节必须全 255（而不是 0）——GDI 忽略它，但会读它的程序要拿到不透明
	for i := dibHeaderSizeV3 + 3; i < len(dib); i += 4 {
		if dib[i] != 255 {
			t.Fatalf("alpha 字节 = %d, want 255", dib[i])
		}
	}

	back, err := dibToPng(dib)
	if err != nil {
		t.Fatalf("dibToPng: %v", err)
	}
	got := decodeTestPNG(t, back)
	assertSamePixels(t, src, got)
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			if a := got.NRGBAAt(x, y).A; a != 255 {
				t.Fatalf("(%d,%d) alpha = %d, want 255", x, y, a)
			}
		}
	}
}

func TestOpaqueImageFromLegacyDib(t *testing.T) {
	// 模拟对端只有老路径：只写 CF_DIB、32bpp、alpha 列留 0（GDI 抓屏的典型产物）。
	// 读回来必须颜色不变、alpha 全 255，而不是一张全透明的空图。
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	want := []color.NRGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
		{R: 128, G: 64, B: 32, A: 255}, {A: 255}, {R: 255, G: 255, B: 255, A: 255},
	}
	for i, c := range want {
		src.SetNRGBA(i%3, i/3, c)
	}
	dib := nrgbaToDibV3(src)
	for i := dibHeaderSizeV3 + 3; i < len(dib); i += 4 {
		dib[i] = 0
	}
	back, err := dibToPng(dib)
	if err != nil {
		t.Fatalf("dibToPng: %v", err)
	}
	assertSamePixels(t, src, decodeTestPNG(t, back))
}

func TestOpaquePngHasNoAlphaChannel(t *testing.T) {
	// 不透明图不该白占一条 alpha 通道：png 编码器对 NRGBA 会走 Opaque() 快路径
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 10, 20, 30, 255
	}
	dib := nrgbaToDibV3(src)
	out, err := dibToPng(dib)
	if err != nil {
		t.Fatal(err)
	}
	// IHDR: 8 字节签名 + 4 长度 + 4 "IHDR" + 4 宽 + 4 高 + 1 位深 + 1 颜色类型
	if len(out) < 26 {
		t.Fatal("png 过短")
	}
	if ct := out[25]; ct != 2 { // 2 = truecolor(RGB), 6 = truecolor+alpha
		t.Fatalf("不透明图 PNG 颜色类型 = %d, want 2", ct)
	}
}

// ---- 读取方向：DIB → PNG ----

func TestAllZeroAlphaTreatedAsOpaque(t *testing.T) {
	// GDI 抓屏 / 截图工具写出来的 32bpp DIB，alpha 整片是 0。
	// 照单全收会让整张图变成全透明，必须按不透明处理。
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 11, 22, 33, 255
	}
	dib := nrgbaToDibV3(src)
	for i := dibHeaderSizeV3 + 3; i < len(dib); i += 4 {
		dib[i] = 0
	}
	out, err := dibToPng(dib)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestPNG(t, out)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			c := got.NRGBAAt(x, y)
			if c.A != 255 {
				t.Fatalf("(%d,%d) alpha = %d, want 255（全 0 alpha 应视为不透明）", x, y, c.A)
			}
			if c.R != 11 || c.G != 22 || c.B != 33 {
				t.Fatalf("(%d,%d) 颜色被改写: %+v", x, y, c)
			}
		}
	}
}

func TestDib24bpp(t *testing.T) {
	// 2x2 24bpp bottom-up，每行 6 字节对齐到 8
	w, h := 2, 2
	rowSize := 8
	dib := make([]byte, dibHeaderSizeV3+rowSize*h)
	putLE32(dib[0:], dibHeaderSizeV3)
	putLE32(dib[4:], uint32(w))
	putLE32(dib[8:], uint32(h))
	putLE16(dib[12:], 1)
	putLE16(dib[14:], 24)
	putLE32(dib[16:], biRGB)
	// 底行：左红 右绿；顶行：左蓝 右白
	put := func(off int, r, g, b byte) { dib[off], dib[off+1], dib[off+2] = b, g, r }
	base := dibHeaderSizeV3
	put(base+0, 255, 0, 0)
	put(base+3, 0, 255, 0)
	put(base+rowSize+0, 0, 0, 255)
	put(base+rowSize+3, 255, 255, 255)

	out, err := dibToPng(dib)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestPNG(t, out)
	// bottom-up：文件里第一行是图像的最底行，所以 y=0 取的是构造时写的第二行
	want := []color.NRGBA{
		{B: 255, A: 255}, {R: 255, G: 255, B: 255, A: 255}, // y=0：蓝、白
		{R: 255, A: 255}, {G: 255, A: 255}, // y=1：红、绿
	}
	for i, c := range want {
		if gc := got.NRGBAAt(i%2, i/2); gc != c {
			t.Fatalf("像素 %d = %+v, want %+v", i, gc, c)
		}
	}
}

func TestDibTopDown(t *testing.T) {
	// biHeight 为负表示 top-down，行序不能反过来
	w, h := 1, 2
	dib := make([]byte, dibHeaderSizeV3+w*4*h)
	putLE32(dib[0:], dibHeaderSizeV3)
	putLE32(dib[4:], uint32(w))
	putLE32(dib[8:], uint32(int32(-h)))
	putLE16(dib[12:], 1)
	putLE16(dib[14:], 32)
	// 第一行（top）红，第二行蓝
	dib[dibHeaderSizeV3+0] = 0
	dib[dibHeaderSizeV3+2] = 255
	dib[dibHeaderSizeV3+3] = 255
	dib[dibHeaderSizeV3+4] = 255
	dib[dibHeaderSizeV3+7] = 255

	out, err := dibToPng(dib)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeTestPNG(t, out)
	if c := got.NRGBAAt(0, 0); c.R != 255 || c.B != 0 {
		t.Fatalf("top-down 首行应为红, got %+v", c)
	}
	if c := got.NRGBAAt(0, 1); c.B != 255 || c.R != 0 {
		t.Fatalf("top-down 次行应为蓝, got %+v", c)
	}
}

func TestDibBitFieldsLegacyMasks(t *testing.T) {
	// 40 字节头 + BI_BITFIELDS：三个 masks 跟在头后面，像素从 52 开始
	w, h := 1, 1
	dib := make([]byte, 52+w*4*h)
	putLE32(dib[0:], dibHeaderSizeV3)
	putLE32(dib[4:], uint32(w))
	putLE32(dib[8:], uint32(h))
	putLE16(dib[12:], 1)
	putLE16(dib[14:], 32)
	putLE32(dib[16:], biBitFields)
	putLE32(dib[40:], 0x00FF0000)
	putLE32(dib[44:], 0x0000FF00)
	putLE32(dib[48:], 0x000000FF)
	dib[52], dib[53], dib[54], dib[55] = 10, 20, 30, 255 // B G R A

	out, err := dibToPng(dib)
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeTestPNG(t, out).NRGBAAt(0, 0); c.R != 30 || c.G != 20 || c.B != 10 {
		t.Fatalf("像素错位: %+v", c)
	}
}

// ---- 畸形输入：只要求不 panic、不越界 ----

func TestDibMalformed(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"空", nil},
		{"过短", make([]byte, 10)},
		{"全零头", make([]byte, 40)},
		{"头的尺寸谎报", func() []byte {
			b := make([]byte, 40)
			putLE32(b[0:], 4096) // biSize 比实际大
			return b
		}()},
		{"宽高离谱", func() []byte {
			b := make([]byte, 40)
			putLE32(b[0:], 40)
			putLE32(b[4:], 0x7FFFFFFF)
			putLE32(b[8:], 0x7FFFFFFF)
			putLE16(b[14:], 32)
			return b
		}()},
		{"biBitFields 但缺 masks", func() []byte {
			b := make([]byte, 40)
			putLE32(b[0:], 40)
			putLE32(b[4:], 4)
			putLE32(b[8:], 4)
			putLE16(b[14:], 32)
			putLE32(b[16:], biBitFields)
			return b
		}()},
		{"像素体被截断", func() []byte {
			b := make([]byte, 60)
			putLE32(b[0:], 40)
			putLE32(b[4:], 100)
			putLE32(b[8:], 100)
			putLE16(b[14:], 32)
			return b
		}()},
		{"不支持的位深", func() []byte {
			b := make([]byte, 200)
			putLE32(b[0:], 40)
			putLE32(b[4:], 4)
			putLE32(b[8:], 4)
			putLE16(b[14:], 8)
			return b
		}()},
		{"调色板数量离谱", func() []byte {
			b := make([]byte, 200)
			putLE32(b[0:], 40)
			putLE32(b[4:], 4)
			putLE32(b[8:], 4)
			putLE16(b[14:], 32)
			putLE32(b[32:], 0xFFFF)
			return b
		}()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 只要不 panic 且不返回"看起来正常"的图就行
			if _, err := dibToPng(c.data); err == nil {
				// "像素体被截断" 这类允许按实际行数裁剪后成功，但必须产出合法 PNG
				if _, err2 := dibToPng(c.data); err2 != nil {
					t.Fatalf("不一致的返回值")
				}
			}
		})
	}
}

func TestDibTruncatedBodyIsCropped(t *testing.T) {
	// 声明 100 行但只给了 3 行：按实际裁剪，输出 3 行，不能崩也不能空转
	w := 1
	dib := make([]byte, dibHeaderSizeV3+w*4*3)
	putLE32(dib[0:], dibHeaderSizeV3)
	putLE32(dib[4:], uint32(w))
	putLE32(dib[8:], 100)
	putLE16(dib[12:], 1)
	putLE16(dib[14:], 32)
	for i := 0; i < 3; i++ {
		dib[dibHeaderSizeV3+i*4+2] = 255
		dib[dibHeaderSizeV3+i*4+3] = 255
	}
	out, err := dibToPng(dib)
	if err != nil {
		t.Fatalf("dibToPng: %v", err)
	}
	got := decodeTestPNG(t, out)
	if got.Rect.Dy() != 3 {
		t.Fatalf("裁剪后高度 = %d, want 3", got.Rect.Dy())
	}
}

// ---- 工具函数 ----

func TestIsPNG(t *testing.T) {
	if !isPNG(encodeTestPNG(t, image.NewNRGBA(image.Rect(0, 0, 1, 1)))) {
		t.Fatal("合法 PNG 未被识别")
	}
	if isPNG([]byte("not a png")) || isPNG(nil) || isPNG([]byte{0x89, 'P', 'N'}) {
		t.Fatal("非 PNG 被误判")
	}
}

func TestDecodePngToNRGBAHandlesSubImage(t *testing.T) {
	// 非零 Min 的图（Rect 不从 0 开始）必须被规整成从 0 开始，
	// 否则调用方按 Pix 下标取值会整体错位
	big := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	sub := big.SubImage(image.Rect(1, 1, 3, 3))
	out, err := decodePngToNRGBA(encodeTestPNG(t, sub))
	if err != nil {
		t.Fatal(err)
	}
	if out.Rect.Min != (image.Point{}) {
		t.Fatalf("Min = %v, want (0,0)", out.Rect.Min)
	}
	if out.Rect.Dx() != 2 || out.Rect.Dy() != 2 {
		t.Fatalf("尺寸 = %v, want 2x2", out.Rect)
	}
}
