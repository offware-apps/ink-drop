package epub

import (
	"bytes"
	"encoding/binary"
	picture "image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
)

// maxPixels bounds an image decoded to be fitted: past it, about 100 MB of
// memory once decoded, the image is packed as it came.
const maxPixels = 24_000_000

// fit returns the image no wider than maxWidth, 0 for any width, and in grey
// when grey is set, re-encoded; a GIF comes back as a PNG of its first frame.
// An image it cannot read, or whose copy is no lighter, stays as it came.
func fit(mt string, data []byte, maxWidth int, grey bool) (string, []byte) {
	if maxWidth <= 0 && !grey || mt != "image/jpeg" && mt != "image/png" && mt != "image/gif" {
		return mt, data
	}
	// A copy keeps no Exif, so a photo the reader turns upright would not be.
	if mt == "image/jpeg" && !upright(data) {
		return mt, data
	}
	cfg, _, err := picture.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return mt, data
	}
	shrinks := maxWidth > 0 && cfg.Width > maxWidth
	if !shrinks && !grey {
		return mt, data
	}
	src, _, err := picture.Decode(bytes.NewReader(data))
	if err != nil {
		return mt, data
	}
	img := src
	if shrinks {
		img = shrink(src, maxWidth, max(1, cfg.Height*maxWidth/cfg.Width))
	}
	if grey {
		// On white, as a reader shows it: a clear pixel read as grey is black.
		g := picture.NewGray(img.Bounds())
		draw.Draw(g, g.Bounds(), picture.White, picture.Point{}, draw.Src)
		draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Over)
		img = g
	}
	var out bytes.Buffer
	outType := mt
	if mt == "image/jpeg" {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 80})
	} else {
		outType = "image/png"
		err = png.Encode(&out, img)
	}
	// Lighter is what fitting is for: a two-colour PNG grows once grey.
	if err != nil || out.Len() >= len(data) {
		return mt, data
	}
	return outType, out.Bytes()
}

// upright reports whether a JPEG shows as it is stored: its Exif orientation,
// where it has one, is 1.
func upright(data []byte) bool {
	// The segments before the image data, each a marker and its length.
	for i := 2; i+4 <= len(data) && data[i] == 0xFF; {
		marker, size := data[i+1], int(data[i+2])<<8|int(data[i+3])
		if marker == 0xDA || size < 2 {
			break
		}
		seg := data[i+4 : min(i+2+size, len(data))]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return orientation(seg[6:]) <= 1
		}
		i += 2 + size
	}
	return true
}

// orientation returns the orientation tag of the TIFF data in an Exif
// segment, 0 where it has none.
func orientation(t []byte) int {
	var o binary.ByteOrder
	switch {
	case len(t) < 8:
		return 0
	case string(t[:2]) == "II":
		o = binary.LittleEndian
	case string(t[:2]) == "MM":
		o = binary.BigEndian
	default:
		return 0
	}
	ifd := int(o.Uint32(t[4:]))
	if ifd < 0 || ifd+2 > len(t) {
		return 0
	}
	for k := range int(o.Uint16(t[ifd:])) {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if o.Uint16(t[e:]) == 0x0112 {
			return int(o.Uint16(t[e+8:]))
		}
	}
	return 0
}

// shrink returns src scaled to w by h, each pixel the mean of the source
// pixels it covers, which keeps thin lines and small text readable.
func shrink(src picture.Image, w, h int) picture.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := picture.NewRGBA(picture.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+max((y+1)*sh/h, y*sh/h+1)
		for x := range w {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), uint16(a / n)})
		}
	}
	return dst
}
