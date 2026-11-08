package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"strings"

	_ "image/gif"
	_ "image/png"
)

// smallPicture makes a small copy (320 pixels wide at most) of a picture given as a
// data: address, for the downloads list. Pictures it cannot read are kept as they are
// when small enough, and dropped otherwise.
func smallPicture(dataURL string) string {
	const maxW = 320
	comma := strings.IndexByte(dataURL, ',')
	if !strings.HasPrefix(dataURL, "data:") || comma < 0 || !strings.Contains(dataURL[:comma], ";base64") {
		return dataURL
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return ""
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		if len(dataURL) <= 400<<10 {
			return dataURL // a format Go cannot read (WebP): small enough to keep
		}
		return ""
	}
	b := img.Bounds()
	if b.Dx() <= maxW && len(dataURL) <= 200<<10 {
		return dataURL
	}
	w, h := b.Dx(), b.Dy()
	if w > maxW {
		h = max(1, h*maxW/w)
		w = maxW
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Box filter: each new pixel is the average of the pixels it covers.
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*b.Dy()/h
		y1 := max(y0+1, b.Min.Y+(y+1)*b.Dy()/h)
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*b.Dx()/w
			x1 := max(x0+1, b.Min.X+(x+1)*b.Dx()/w)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := img.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}
