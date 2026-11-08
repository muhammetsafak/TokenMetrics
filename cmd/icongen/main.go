// Command icongen renders the TokenMetrics "T-meter" brand mark into every
// raster and vector asset the app needs. It uses only the standard library and
// is deterministic: the same code always yields byte-identical files.
//
// Run it from the repository root (see the go:generate line in main.go):
//
//	go run ./cmd/icongen
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	colorTile   = color.NRGBA{R: 0x0E, G: 0x10, B: 0x13, A: 255}
	colorWhite  = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 255}
	colorTop    = color.NRGBA{R: 0xF4, G: 0xF4, B: 0xF1, A: 255}
	colorAccent = color.NRGBA{R: 0x5B, G: 0x8D, B: 0xFF, A: 255}
	colorAlert  = color.NRGBA{R: 0xF0, G: 0xA4, B: 0x28, A: 255}
	colorBlack  = color.NRGBA{R: 0, G: 0, B: 0, A: 255}
)

// Output paths, relative to the -out directory.
const (
	pathAppIcon          = "build/appicon.png"
	pathWindowsIcon      = "build/windows/icon.ico"
	pathTrayPNG          = "build/tray/tray.png"
	pathTrayICO          = "build/tray/tray.ico"
	pathTrayAlertPNG     = "build/tray/tray-alert.png"
	pathTrayAlertICO     = "build/tray/tray-alert.ico"
	pathTrayTemplate     = "build/tray/tray-template.png"
	pathTrayTemplateWarn = "build/tray/tray-template-alert.png"
	pathMarkSVG          = "frontend/src/assets/brand/mark.svg"
	pathMarkTileSVG      = "frontend/src/assets/brand/mark-tile.svg"
)

var (
	windowsIconSizes = []int{16, 24, 32, 48, 64, 128, 256}
	trayIconSizes    = []int{16, 24, 32, 48, 64}
)

// ---------------------------------------------------------------------------
// Geometry (64x64 viewBox for the app icon).
// ---------------------------------------------------------------------------

func tileShapes() []shape {
	return []shape{
		{x: 2, y: 2, w: 60, h: 60, r: 14, col: colorTile, opacity: 1},
		// 1-unit inner line, 8% white.
		{x: 2.5, y: 2.5, w: 59, h: 59, r: 13.5, col: colorWhite, opacity: 0.08, stroke: 1},
	}
}

// markShapes returns the mark for the given target size. topCol is the colour
// of the top bar. Larger sizes get the full three-slice mark; smaller ones are
// simplified so they stay legible.
func markShapes(size int, topCol color.NRGBA) []shape {
	switch {
	case size >= 48:
		return []shape{
			{x: 16, y: 15, w: 32, h: 8, r: 4, col: topCol, opacity: 1},
			{x: 28, y: 26.5, w: 8, h: 6.5, r: 2.6, col: colorAccent, opacity: 1},
			{x: 28, y: 35.5, w: 8, h: 6.5, r: 2.6, col: colorAccent, opacity: 0.62},
			{x: 28, y: 44.5, w: 8, h: 6.5, r: 2.6, col: colorAccent, opacity: 0.30},
		}
	case size >= 24:
		return []shape{
			{x: 14, y: 13, w: 36, h: 10, r: 5, col: topCol, opacity: 1},
			{x: 27, y: 27, w: 10, h: 9, r: 3, col: colorAccent, opacity: 1},
			{x: 27, y: 40, w: 10, h: 9, r: 3, col: colorAccent, opacity: 0.5},
		}
	default:
		return []shape{
			{x: 12, y: 12, w: 40, h: 12, r: 6, col: topCol, opacity: 1},
			{x: 26, y: 28, w: 12, h: 24, r: 5, col: colorAccent, opacity: 1},
		}
	}
}

// appIconImage renders the tile + mark so the tile fills the whole canvas
// edge to edge (used by the Windows ICO). The tile is 60 viewBox units wide
// and starts at x=2, so scale by the tile width and shift the 2-unit margin
// off-canvas.
func appIconImage(size int) image.Image {
	scale := float64(size) / 60
	shapes := append(tileShapes(), markShapes(size, colorTop)...)
	return render(size, scale, -2*scale, shapes)
}

// macIconImage renders the macOS app icon: the tile itself (not the whole
// 64-unit viewBox) occupies ~80% of the canvas (824 of 1024 px), centred, with
// a transparent margin and no shadow.
func macIconImage(size int) image.Image {
	tile := float64(size) * 824 / 1024
	// The tile is 60 viewBox units wide and starts at x=2, so scale by the tile
	// width and shift so that viewBox x=2 lands on the margin edge.
	scale := tile / 60
	margin := (float64(size) - tile) / 2
	offset := margin - 2*scale
	shapes := append(tileShapes(), markShapes(64, colorTop)...)
	return render(size, scale, offset, shapes)
}

// trayShapes: 16x16 grid colour tray icon.
func trayShapes(column color.NRGBA) []shape {
	return []shape{
		{x: 0.5, y: 0.5, w: 15, h: 15, r: 3.5, col: colorTile, opacity: 1},
		// 18% white line, drawn just inside the tile edge.
		{x: 1, y: 1, w: 14, h: 14, r: 3, col: colorWhite, opacity: 0.18, stroke: 1},
		{x: 3, y: 3, w: 10, h: 3, r: 1.5, col: colorTop, opacity: 1},
		{x: 6.5, y: 7, w: 3, h: 6, r: 1.3, col: column, opacity: 1},
	}
}

func trayImage(column color.NRGBA) func(int) image.Image {
	return func(size int) image.Image {
		return render(size, float64(size)/16, 0, trayShapes(column))
	}
}

// templateShapes: 22x22 grid macOS template (black + alpha) tray icon.
func templateShapes(alert bool) []shape {
	shapes := []shape{
		{x: 3, y: 3, w: 16, h: 4, r: 2, col: colorBlack, opacity: 1},
		{x: 8.75, y: 9, w: 4.5, h: 3.5, r: 1.4, col: colorBlack, opacity: 1},
		{x: 8.75, y: 13.75, w: 4.5, h: 3.5, r: 1.4, col: colorBlack, opacity: 0.6},
	}
	if alert {
		// Filled dot at the bottom right, beside the slices, centre
		// (17.5, 15), radius 2.75. It keeps >= 1.5 grid units of transparent
		// space from the slices (x <= 13.25) and the top bar (y <= 7).
		shapes = append(shapes, shape{x: 14.75, y: 12.25, w: 5.5, h: 5.5, r: 2.75, col: colorBlack, opacity: 1})
	}
	return shapes
}

func templateImage(alert bool) image.Image {
	return render(44, 2, 0, templateShapes(alert))
}

// ---------------------------------------------------------------------------
// SVG output.
// ---------------------------------------------------------------------------

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func hex(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

func svgRect(s shape, fill string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s"`, num(s.x), num(s.y), num(s.w), num(s.h), num(s.r))
	if s.stroke > 0 {
		fmt.Fprintf(&b, ` fill="none" stroke="%s" stroke-width="%s"`, fill, num(s.stroke))
		if s.opacity != 1 {
			fmt.Fprintf(&b, ` stroke-opacity="%s"`, num(s.opacity))
		}
	} else {
		fmt.Fprintf(&b, ` fill="%s"`, fill)
		if s.opacity != 1 {
			fmt.Fprintf(&b, ` fill-opacity="%s"`, num(s.opacity))
		}
	}
	b.WriteString("/>")
	return b.String()
}

func svgDocument(title string, body []string) []byte {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="` + title + `">` + "\n")
	for _, line := range body {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func markSVG() []byte {
	var body []string
	for i, s := range markShapes(64, colorTop) {
		fill := hex(s.col)
		if i == 0 {
			fill = "currentColor"
		}
		body = append(body, svgRect(s, fill))
	}
	return svgDocument("TokenMetrics", body)
}

func markTileSVG() []byte {
	var body []string
	for _, s := range tileShapes() {
		body = append(body, svgRect(s, hex(s.col)))
	}
	for _, s := range markShapes(64, colorTop) {
		body = append(body, svgRect(s, hex(s.col)))
	}
	return svgDocument("TokenMetrics", body)
}

// ---------------------------------------------------------------------------
// Generation.
// ---------------------------------------------------------------------------

// generate builds every output file in memory, keyed by slash-separated path
// relative to the output directory.
func generate() (map[string][]byte, error) {
	files := map[string][]byte{}

	put := func(path string, img image.Image) error {
		data, err := encodePNG(img)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		files[path] = data
		return nil
	}
	putICO := func(path string, sizes []int, imageFor func(int) image.Image) error {
		data, err := encodeICO(sizes, imageFor)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		files[path] = data
		return nil
	}

	if err := put(pathAppIcon, macIconImage(1024)); err != nil {
		return nil, err
	}
	if err := putICO(pathWindowsIcon, windowsIconSizes, appIconImage); err != nil {
		return nil, err
	}
	if err := put(pathTrayPNG, trayImage(colorAccent)(64)); err != nil {
		return nil, err
	}
	if err := putICO(pathTrayICO, trayIconSizes, trayImage(colorAccent)); err != nil {
		return nil, err
	}
	if err := put(pathTrayAlertPNG, trayImage(colorAlert)(64)); err != nil {
		return nil, err
	}
	if err := putICO(pathTrayAlertICO, trayIconSizes, trayImage(colorAlert)); err != nil {
		return nil, err
	}
	if err := put(pathTrayTemplate, templateImage(false)); err != nil {
		return nil, err
	}
	if err := put(pathTrayTemplateWarn, templateImage(true)); err != nil {
		return nil, err
	}
	files[pathMarkSVG] = markSVG()
	files[pathMarkTileSVG] = markTileSVG()
	return files, nil
}

func writeAll(out string, files map[string][]byte) error {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, files[p], 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", p)
	}
	return nil
}

func main() {
	out := flag.String("out", ".", "repository root to write assets into")
	flag.Parse()

	files, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
	if err := writeAll(*out, files); err != nil {
		fmt.Fprintln(os.Stderr, "icongen:", err)
		os.Exit(1)
	}
}
