// genicon renders the PowerDeck icon (the same design as internal/localui/web/icon.svg) with
// signed-distance anti-aliasing and writes the Windows .ico plus PNGs. Run from the windows/ folder:
//
//	go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

// Design in a 108×108 box (matches the SVG).
const (
	box      = 108.0
	corner   = 26.0
	stroke   = 9.0
	cx, cy   = 54.0, 54.84
	radius   = 24.0
	lineTop  = 26.0
	lineBot  = 52.0
	gapAngle = 49.5 // degrees either side of straight up left open by the arc
)

var (
	c1 = [3]float64{0x5B, 0x4C, 0xF0}
	c2 = [3]float64{0x9B, 0x5C, 0xF6}
)

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := clamp(((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func arcDist(px, py float64) float64 {
	// Angle from straight up, clockwise, in degrees (screen coordinates, y down).
	a := math.Atan2(px-cx, -(py - cy)) * 180 / math.Pi
	if math.Abs(a) >= gapAngle {
		return math.Abs(math.Hypot(px-cx, py-cy) - radius)
	}
	end := func(deg float64) (float64, float64) {
		r := deg * math.Pi / 180
		return cx + radius*math.Sin(r), cy - radius*math.Cos(r)
	}
	ex1, ey1 := end(-gapAngle)
	ex2, ey2 := end(gapAngle)
	return math.Min(math.Hypot(px-ex1, py-ey1), math.Hypot(px-ex2, py-ey2))
}

func roundedBox(px, py float64) float64 {
	h := box/2 - corner
	qx, qy := math.Abs(px-box/2)-h, math.Abs(py-box/2)-h
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - corner
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := box / float64(size) // design units per pixel
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)*s, (float64(y)+0.5)*s
			alpha := clamp(0.5 - roundedBox(px, py)/s)
			if alpha == 0 {
				continue
			}
			d := math.Min(segDist(px, py, cx, lineTop, cx, lineBot), arcDist(px, py))
			glyph := clamp(0.5 + (stroke/2-d)/s)
			t := clamp((px + py) / (2 * box))
			var c [3]uint8
			for i := range c {
				bg := c1[i] + (c2[i]-c1[i])*t
				c[i] = uint8(math.Round(bg + (255-bg)*glyph))
			}
			img.SetNRGBA(x, y, color.NRGBA{c[0], c[1], c[2], uint8(math.Round(alpha * 255))})
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

// writeICO stores each size as a PNG entry (supported since Windows Vista).
func writeICO(path string, sizes []int) {
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, pngBytes(render(s)))
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		_ = binary.Write(&b, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, im := range imgs {
		b.Write(im)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	out := "assets"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	writeICO(filepath.Join(out, "powerdeck.ico"), []int{16, 20, 24, 32, 40, 48, 64, 128, 256})
	for _, s := range []int{256, 512} {
		if err := os.WriteFile(filepath.Join(out, "icon-"+strconv.Itoa(s)+".png"), pngBytes(render(s)), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Println("wrote assets/powerdeck.ico, assets/icon-256.png, assets/icon-512.png")
}
