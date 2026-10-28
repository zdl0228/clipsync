//go:build darwin

package main

// 真实剪贴板往返验证：确认图片写得进、读得出，且像素不被系统接口这一层偷偷改写。
//
// 会动用户的系统剪贴板（结束时恢复原内容），所以默认跳过。
// 需要时手动跑：
//   CLIPSYNC_MANUAL_TEST=1 go test -run TestManualPasteboard -v .
//
// 单测（dib_test.go）覆盖的是转换逻辑，这里补的是"系统这一层有没有动过手脚"，
// 两者不重复。两个用例要一起看：
//   - 带透明：alpha 梯度必须原样回来（原 bug 就是在这里被拍成 255）
//   - 不透明：颜色必须一个不差，且不能被启发式误判成半透明/全透明

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

func TestManualPasteboardImageRoundTrip(t *testing.T) {
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

	// 带透明的图：同一颜色，alpha 从 0 递增到 192。
	// 只有真 alpha 才能还原这个梯度；被拍成不透明时最右像素会变成 255。
	withAlpha := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	alphaWant := make([]color.NRGBA, 4)
	for x := 0; x < 4; x++ {
		alphaWant[x] = color.NRGBA{R: 200, G: 100, B: 50, A: uint8(x * 64)}
		withAlpha.SetNRGBA(x, 0, alphaWant[x])
	}

	// 不透明图：普通截图/照片走的就是这条路径（不写 CF_DIBV5）。
	// 它不能被修 alpha 的改动弄坏——逐像素必须一个不差。
	opaque := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	opaqueWant := make([]color.NRGBA, 8)
	for i := 0; i < 8; i++ {
		opaqueWant[i] = color.NRGBA{
			R: uint8(20 + i*25), G: uint8(200 - i*15), B: uint8(10 + i*30), A: 255,
		}
		opaque.SetNRGBA(i%4, i/4, opaqueWant[i])
	}

	cases := []struct {
		name string
		img  *image.NRGBA
		want []color.NRGBA
	}{
		{"带透明", withAlpha, alphaWant},
		{"完全不透明", opaque, opaqueWant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var enc bytes.Buffer
			if err := png.Encode(&enc, tc.img); err != nil {
				t.Fatal(err)
			}
			if err := c.SetImageSafe(enc.Bytes()); err != nil {
				t.Fatalf("SetImageSafe: %v", err)
			}
			time.Sleep(400 * time.Millisecond)

			mime, body := c.ReadCurrent()
			if mime != "image/png" {
				t.Fatalf("读回类型 = %q, want image/png", mime)
			}
			back, err := png.Decode(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("读回的不是合法 PNG: %v", err)
			}
			b := back.Bounds()
			if b.Dx() != tc.img.Rect.Dx() || b.Dy() != tc.img.Rect.Dy() {
				t.Fatalf("尺寸 = %dx%d, want %dx%d", b.Dx(), b.Dy(), tc.img.Rect.Dx(), tc.img.Rect.Dy())
			}
			// 注意用 NRGBAModel 取非预乘值：At().RGBA() 返回的是预乘后的，
			// 直接看会误以为颜色被压暗了（200,alpha=192 会显示成 151）。
			for i, want := range tc.want {
				x, y := i%b.Dx(), i/b.Dx()
				got := color.NRGBAModel.Convert(back.At(x, y)).(color.NRGBA)
				if got != want {
					t.Fatalf("(%d,%d) = %+v, want %+v", x, y, got, want)
				}
			}
			t.Logf("%s: %d 个像素全部一致", tc.name, len(tc.want))
		})
	}
}
