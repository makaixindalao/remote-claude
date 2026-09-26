package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"sync"
)

// 应用图标的 PNG：通知里的图标（安卓不认 SVG）、加到主屏幕的图标（iOS 只认 PNG）、安卓状态栏的小图标。
// 和侧栏的标志（components/logo.tsx）是同一个图形：陶土色圆角方块里一颗 ✻。第一次要的时候画一遍，缓存起来

var iconSizes = map[string]struct {
	size  int
	badge bool // 状态栏小图标：只要形状，白色、透明底
	full  bool // 不要圆角（iOS 自己会切圆角）
}{
	"/icon-192.png":         {size: 192},
	"/icon-512.png":         {size: 512},
	"/apple-touch-icon.png": {size: 180, full: true},
	"/badge-96.png":         {size: 96, badge: true},
}

var iconCache sync.Map

func handleIcon(w http.ResponseWriter, r *http.Request) {
	spec, ok := iconSizes[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, ok := iconCache.Load(r.URL.Path)
	if !ok {
		var buf bytes.Buffer
		_ = png.Encode(&buf, drawIcon(spec.size, spec.badge, spec.full))
		data, _ = iconCache.LoadOrStore(r.URL.Path, buf.Bytes())
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data.([]byte))
}

// drawIcon 按 32×32 的设计稿画：圆角 7，四根线从 (16,8) 到 (16,24)、线宽 3、圆头，各转 0/45/90/135 度。
// 每个像素取 4×4 个采样点算覆盖率，边缘是抗锯齿的
func drawIcon(size int, badge, full bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := color.NRGBA{0xb7, 0x52, 0x2f, 0xff}
	scale := 32 / float64(size)
	const n = 4
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var inBox, inStar int
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/n) * scale
					y := (float64(py) + (float64(sy)+0.5)/n) * scale
					if full || inRoundRect(x, y, 32, 7) {
						inBox++
					}
					if inStarShape(x, y) {
						inStar++
					}
				}
			}
			box, star := float64(inBox)/(n*n), float64(inStar)/(n*n)
			if badge {
				img.SetNRGBA(px, py, color.NRGBA{0xff, 0xff, 0xff, uint8(star * 255)})
				continue
			}
			// 白色的星叠在陶土色底上；底的边缘按覆盖率透明
			c := color.NRGBA{
				R: uint8(float64(bg.R)*(1-star) + 255*star),
				G: uint8(float64(bg.G)*(1-star) + 255*star),
				B: uint8(float64(bg.B)*(1-star) + 255*star),
				A: uint8(box * 255),
			}
			img.SetNRGBA(px, py, c)
		}
	}
	return img
}

func inRoundRect(x, y, size, r float64) bool {
	cx := math.Max(r, math.Min(size-r, x))
	cy := math.Max(r, math.Min(size-r, y))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

// inStarShape：点离四根线段中任意一根的距离不超过半个线宽
func inStarShape(x, y float64) bool {
	for _, deg := range []float64{0, 45, 90, 135} {
		a := deg * math.Pi / 180
		dx, dy := math.Sin(a)*8, -math.Cos(a)*8 // 从中心 (16,16) 往两头各 8
		if segDist(x, y, 16-dx, 16-dy, 16+dx, 16+dy) <= 1.5 {
			return true
		}
	}
	return false
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	t := ((px-ax)*vx + (py-ay)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}
