package main

import (
	"image"
	"image/color"
	"math"
)

// superSamples is the per-axis sample count used for anti-aliasing
// (4x4 = 16 samples per pixel).
const superSamples = 4

// shape is a rounded rectangle expressed in grid units. A circle is a
// rounded rectangle whose radius is half of its side. When stroke is zero the
// shape is filled; otherwise only a centered stroke of that width is drawn.
type shape struct {
	x, y, w, h float64
	r          float64
	col        color.NRGBA // alpha channel of col is ignored; use opacity
	opacity    float64
	stroke     float64
}

// signedDistance returns the signed distance from (px, py) to the rounded
// rectangle outline; negative values are inside.
func (s shape) signedDistance(px, py float64) float64 {
	cx := s.x + s.w/2
	cy := s.y + s.h/2
	qx := math.Abs(px-cx) - (s.w/2 - s.r)
	qy := math.Abs(py-cy) - (s.h/2 - s.r)
	ox := math.Max(qx, 0)
	oy := math.Max(qy, 0)
	outside := math.Sqrt(float64(ox*ox) + float64(oy*oy))
	inside := math.Min(math.Max(qx, qy), 0)
	return outside + inside - s.r
}

// covers reports whether the point lies on the painted area of the shape.
func (s shape) covers(px, py float64) bool {
	d := s.signedDistance(px, py)
	if s.stroke > 0 {
		return math.Abs(d) <= s.stroke/2
	}
	return d <= 0
}

// render rasterizes shapes onto a size x size canvas. Grid coordinates map to
// pixels as pixel = offset + grid*scale. Shapes are composited in order with
// source-over blending; the result is non-premultiplied NRGBA.
func render(size int, scale, offset float64, shapes []shape) *image.NRGBA {
	// Premultiplied accumulation buffer: r, g, b, a in [0, 1].
	buf := make([]float64, size*size*4)

	for _, s := range shapes {
		// Restrict work to the shape's bounding box (plus stroke and a margin).
		pad := s.stroke/2 + 1/scale
		x0 := clampInt(int(math.Floor(offset+(s.x-pad)*scale)), 0, size)
		x1 := clampInt(int(math.Ceil(offset+(s.x+s.w+pad)*scale)), 0, size)
		y0 := clampInt(int(math.Floor(offset+(s.y-pad)*scale)), 0, size)
		y1 := clampInt(int(math.Ceil(offset+(s.y+s.h+pad)*scale)), 0, size)

		sr := float64(s.col.R) / 255
		sg := float64(s.col.G) / 255
		sb := float64(s.col.B) / 255

		for py := y0; py < y1; py++ {
			for px := x0; px < x1; px++ {
				hits := 0
				for sy := 0; sy < superSamples; sy++ {
					for sx := 0; sx < superSamples; sx++ {
						fx := float64(px) + (float64(sx)+0.5)/superSamples
						fy := float64(py) + (float64(sy)+0.5)/superSamples
						gx := (fx - offset) / scale
						gy := (fy - offset) / scale
						if s.covers(gx, gy) {
							hits++
						}
					}
				}
				if hits == 0 {
					continue
				}
				a := s.opacity * float64(hits) / float64(superSamples*superSamples)
				i := (py*size + px) * 4
				inv := 1 - a
				// float64() conversions stop the compiler from fusing
				// multiply-add (FMA), keeping results identical across CPUs.
				buf[i+0] = float64(sr*a) + float64(buf[i+0]*inv)
				buf[i+1] = float64(sg*a) + float64(buf[i+1]*inv)
				buf[i+2] = float64(sb*a) + float64(buf[i+2]*inv)
				buf[i+3] = a + float64(buf[i+3]*inv)
			}
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size*size; i++ {
		a := buf[i*4+3]
		if a <= 0 {
			continue
		}
		o := i * 4
		img.Pix[o+0] = toByte(buf[i*4+0] / a)
		img.Pix[o+1] = toByte(buf[i*4+1] / a)
		img.Pix[o+2] = toByte(buf[i*4+2] / a)
		img.Pix[o+3] = toByte(a)
	}
	return img
}

func toByte(v float64) uint8 {
	v = math.Round(v * 255)
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
