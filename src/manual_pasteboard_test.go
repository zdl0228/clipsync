//go:build darwin

package main

// 真实剪贴板往返验证：确认带 alpha 的 PNG 写得进、读得出。
//
// 会动用户的系统剪贴板（结束时恢复原内容），所以默认跳过。
// 需要时手动跑：
//   CLIPSYNC_MANUAL_TEST=1 go test -run TestManualPasteboardAlphaRoundTrip -v .
//
// 单测（dib_test.go）已经覆盖了转换逻辑，这里补的是"系统接口这一层有没有
// 偷偷把 alpha 拍平"，两者不重复。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

func TestManualPasteboardAlphaRoundTrip(t *testing.T) {
	if os.Getenv("CLIPSYNC_MANUAL_TEST") == "" {
		t.Skip("跳过：需 CLIPSYNC_MANUAL_TEST=1（会占用系统剪贴板，虽然会恢复）")
	}
	c := NewClipboard()
	origMime, origBody := c.ReadCurrent()
	defer func() {
		switch origMime {
		case "text/plain":
			c.SetText(string(origBody))
		case "image/png":
			c.SetImageSafe(origBody)
		}
		t.Logf("已恢复原剪贴板内容 (%s, %d bytes)", origMime, len(origBody))
	}()

	src := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := 0; x < 4; x++ {
		// 同一颜色、alpha 从 0 递增到 192：只有真 alpha 才能还原这个梯度
		src.SetNRGBA(x, 0, color.NRGBA{R: 200, G: 100, B: 50, A: uint8(x * 64)})
	}
	var enc bytes.Buffer
	if err := png.Encode(&enc, src); err != nil {
		t.Fatal(err)
	}

	if err := c.SetImageSafe(enc.Bytes()); err != nil {
		t.Fatalf("SetImageSafe: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	mime, body := c.ReadCurrent()
	t.Logf("读回 mime=%s, %d bytes", mime, len(body))
	if mime != "image/png" {
		t.Fatalf("读回类型 = %q, want image/png", mime)
	}
	back, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("读回的不是合法 PNG: %v", err)
	}
	// 注意用 NRGBAModel 取非预乘值：At().RGBA() 返回的是预乘后的，
	// 直接看会误以为颜色被压暗了（200,alpha=192 会显示成 151）。
	for x := 0; x < 4; x++ {
		c := color.NRGBAModel.Convert(back.At(x, 0)).(color.NRGBA)
		t.Logf("x=%d NRGBA=%d,%d,%d,%d", x, c.R, c.G, c.B, c.A)
		want := color.NRGBA{R: 200, G: 100, B: 50, A: uint8(x * 64)}
		if c != want {
			t.Fatalf("x=%d 像素 = %+v, want %+v", x, c, want)
		}
	}
}
