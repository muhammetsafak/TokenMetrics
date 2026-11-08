package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

func mustGenerate(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return files
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

type icoEntry struct {
	width, height int
	data          []byte
}

func parseICO(t *testing.T, data []byte) []icoEntry {
	t.Helper()
	if len(data) < 6 {
		t.Fatalf("ico too short: %d bytes", len(data))
	}
	if r, typ := binary.LittleEndian.Uint16(data[0:]), binary.LittleEndian.Uint16(data[2:]); r != 0 || typ != 1 {
		t.Fatalf("bad ICONDIR: reserved=%d type=%d", r, typ)
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	entries := make([]icoEntry, 0, count)
	for i := 0; i < count; i++ {
		e := data[6+16*i : 6+16*(i+1)]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		size := int(binary.LittleEndian.Uint32(e[8:]))
		off := int(binary.LittleEndian.Uint32(e[12:]))
		if off+size > len(data) {
			t.Fatalf("entry %d out of range", i)
		}
		entries = append(entries, icoEntry{w, h, data[off : off+size]})
	}
	return entries
}

func TestPNGDimensions(t *testing.T) {
	files := mustGenerate(t)
	want := map[string]int{
		pathAppIcon:          1024,
		pathTrayPNG:          64,
		pathTrayAlertPNG:     64,
		pathTrayTemplate:     44,
		pathTrayTemplateWarn: 44,
	}
	for path, size := range want {
		b := decodePNG(t, files[path]).Bounds()
		if b.Dx() != size || b.Dy() != size {
			t.Errorf("%s: got %dx%d, want %dx%d", path, b.Dx(), b.Dy(), size, size)
		}
	}
}

func TestICOHeaders(t *testing.T) {
	files := mustGenerate(t)
	cases := map[string][]int{
		pathWindowsIcon:  windowsIconSizes,
		pathTrayICO:      trayIconSizes,
		pathTrayAlertICO: trayIconSizes,
	}
	for path, sizes := range cases {
		entries := parseICO(t, files[path])
		if len(entries) != len(sizes) {
			t.Fatalf("%s: got %d entries, want %d", path, len(entries), len(sizes))
		}
		for i, e := range entries {
			if e.width != sizes[i] || e.height != sizes[i] {
				t.Errorf("%s entry %d: got %dx%d, want %d", path, i, e.width, e.height, sizes[i])
			}
			b := decodePNG(t, e.data).Bounds()
			if b.Dx() != sizes[i] || b.Dy() != sizes[i] {
				t.Errorf("%s entry %d: embedded png is %dx%d, want %d", path, i, b.Dx(), b.Dy(), sizes[i])
			}
		}
	}
	// 256 px entries must be stored as 0 in the header bytes.
	data := files[pathWindowsIcon]
	last := 6 + 16*(len(windowsIconSizes)-1)
	if data[last] != 0 || data[last+1] != 0 {
		t.Errorf("256 px entry width/height bytes = %d/%d, want 0/0", data[last], data[last+1])
	}
}

func TestAppIconCornerTransparent(t *testing.T) {
	img := decodePNG(t, mustGenerate(t)[pathAppIcon])
	for _, p := range []image.Point{{0, 0}, {1023, 0}, {0, 1023}, {1023, 1023}} {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a != 0 {
			t.Errorf("pixel %v alpha = %d, want 0", p, a)
		}
	}
	// The centre of the tile must be opaque.
	if _, _, _, a := img.At(512, 700).RGBA(); a == 0 {
		t.Errorf("tile interior is transparent")
	}
}

func TestTemplateIconsAreBlackWithAlpha(t *testing.T) {
	files := mustGenerate(t)
	for _, path := range []string{pathTrayTemplate, pathTrayTemplateWarn} {
		img := decodePNG(t, files[path]).(*image.NRGBA)
		opaque := false
		for i := 0; i < len(img.Pix); i += 4 {
			if img.Pix[i+3] == 0 {
				continue
			}
			opaque = true
			if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
				t.Fatalf("%s: non-black pixel with alpha %d", path, img.Pix[i+3])
			}
		}
		if !opaque {
			t.Errorf("%s: no visible pixels", path)
		}
	}
	// The alert variant adds a dot centred at (17.5, 15) grid -> (35, 30) px,
	// bottom right beside the slices, separate from the top bar.
	plain := decodePNG(t, files[pathTrayTemplate]).(*image.NRGBA)
	alert := decodePNG(t, files[pathTrayTemplateWarn]).(*image.NRGBA)
	if plain.NRGBAAt(35, 30).A != 0 || alert.NRGBAAt(35, 30).A != 255 {
		t.Errorf("alert dot missing: plain A=%d alert A=%d", plain.NRGBAAt(35, 30).A, alert.NRGBAAt(35, 30).A)
	}
	// Every pixel that differs must lie inside the dot's bounding box
	// (x 29..40, y 24..35 px): the top bar and slices are untouched.
	for y := 0; y < 44; y++ {
		for x := 0; x < 44; x++ {
			if plain.NRGBAAt(x, y) == alert.NRGBAAt(x, y) {
				continue
			}
			if x < 29 || x > 40 || y < 24 || y > 35 {
				t.Errorf("pixel (%d,%d) differs outside the dot area", x, y)
			}
		}
	}
	// Transparent gap between the slices (right edge at x=26.5 px) and the dot
	// (left edge at x=29.5 px), and between the top bar (bottom y=14 px) and the dot.
	if alert.NRGBAAt(28, 30).A != 0 {
		t.Errorf("no gap between slice and dot: A=%d at (28,30)", alert.NRGBAAt(28, 30).A)
	}
	if alert.NRGBAAt(35, 20).A != 0 {
		t.Errorf("no gap between top bar and dot: A=%d at (35,20)", alert.NRGBAAt(35, 20).A)
	}
}

// TestWindowsIconTileFillsCanvas checks that the ICO tile reaches the canvas
// edge at every size (no 2/64 margin): the edge midpoints are fully opaque
// while the rounded corners stay transparent.
func TestWindowsIconTileFillsCanvas(t *testing.T) {
	entries := parseICO(t, mustGenerate(t)[pathWindowsIcon])
	for _, e := range entries {
		img := decodePNG(t, e.data).(*image.NRGBA)
		n, mid := e.width, e.width/2
		for _, p := range []image.Point{{0, mid}, {n - 1, mid}, {mid, 0}, {mid, n - 1}} {
			if a := img.NRGBAAt(p.X, p.Y).A; a != 255 {
				t.Errorf("%dpx: edge pixel %v alpha = %d, want 255", n, p, a)
			}
		}
		if a := img.NRGBAAt(0, 0).A; a != 0 {
			t.Errorf("%dpx: corner alpha = %d, want 0 (rounded)", n, a)
		}
	}
}

func TestDeterministic(t *testing.T) {
	a := mustGenerate(t)
	b := mustGenerate(t)
	if len(a) != len(b) {
		t.Fatalf("file count differs: %d vs %d", len(a), len(b))
	}
	for path, data := range a {
		if !bytes.Equal(data, b[path]) {
			t.Errorf("%s differs between runs", path)
		}
	}
}

func TestSVGsWellFormed(t *testing.T) {
	files := mustGenerate(t)
	if !bytes.Contains(files[pathMarkSVG], []byte(`fill="currentColor"`)) {
		t.Error("mark.svg: top bar must use currentColor")
	}
	if bytes.Contains(files[pathMarkSVG], []byte("#0E1013")) {
		t.Error("mark.svg: must not contain the tile")
	}
	if !bytes.Contains(files[pathMarkTileSVG], []byte("#0E1013")) {
		t.Error("mark-tile.svg: missing tile")
	}
}

// TestAppIconTileSpan guards the Apple grid: the tile body must span 824 px of
// the 1024 px canvas (x = 100..923), not the whole viewBox scaled to 824.
func TestAppIconTileSpan(t *testing.T) {
	img := decodePNG(t, mustGenerate(t)[pathAppIcon]).(*image.NRGBA)
	// Row 512 crosses the tile's straight edges (clear of the rounded corners).
	first, last := -1, -1
	for x := 0; x < 1024; x++ {
		if img.NRGBAAt(x, 512).A > 127 {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	if first < 99 || first > 101 || last < 922 || last > 924 {
		t.Errorf("tile spans x=%d..%d on row 512, want ~100..923", first, last)
	}
	// Same check vertically on column 512.
	first, last = -1, -1
	for y := 0; y < 1024; y++ {
		if img.NRGBAAt(512, y).A > 127 {
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	if first < 99 || first > 101 || last < 922 || last > 924 {
		t.Errorf("tile spans y=%d..%d on column 512, want ~100..923", first, last)
	}
}

// TestMarkSizeBands pins which simplification band each icon size uses.
func TestMarkSizeBands(t *testing.T) {
	cases := []struct{ size, shapes int }{
		{16, 2}, {20, 2}, {23, 2},
		{24, 3}, {32, 3}, {40, 3}, {47, 3},
		{48, 4}, {64, 4}, {256, 4},
	}
	for _, c := range cases {
		if got := len(markShapes(c.size, colorTop)); got != c.shapes {
			t.Errorf("markShapes(%d): got %d shapes, want %d", c.size, got, c.shapes)
		}
	}
}
