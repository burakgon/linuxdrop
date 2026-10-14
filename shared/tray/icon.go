package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// iconPNG renders a simple filled-circle status icon: teal when actively
// syncing, gray otherwise. Generated in code so there are no binary assets.
func iconPNG(active bool) []byte {
	const s = 64
	fg := color.RGBA{0x9E, 0x9E, 0x9E, 0xFF} // gray
	if active {
		fg = color.RGBA{0x1E, 0x6F, 0x5C, 0xFF} // brand teal
	}
	img := image.NewRGBA(image.Rect(0, 0, s, s))
	const c = 32
	const r2 = 30 * 30
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx, dy := x-c, y-c
			if dx*dx+dy*dy <= r2 {
				img.SetRGBA(x, y, fg)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
