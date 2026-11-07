package epub

import (
	"bytes"
	picture "image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// photo returns a noisy picture, which compresses as a photograph does.
func photo(w, h int) picture.Image {
	img := picture.NewRGBA(picture.Rect(0, 0, w, h))
	seed := uint32(1)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{uint8(seed >> 8), uint8(seed >> 16), uint8(seed >> 24), 255})
		}
	}
	return img
}

func encode(t *testing.T, img picture.Image, kind string) []byte {
	var b bytes.Buffer
	var err error
	switch kind {
	case "jpeg":
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	case "png":
		err = png.Encode(&b, img)
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFitShrinksAndGreys(t *testing.T) {
	data := encode(t, photo(3000, 200), "jpeg")
	mt, out := fit("image/jpeg", data, 1000, false)
	cfg, _, err := picture.DecodeConfig(bytes.NewReader(out))
	if err != nil || mt != "image/jpeg" || cfg.Width != 1000 || cfg.Height != 66 {
		t.Fatalf("shrink: %s %dx%d, %v", mt, cfg.Width, cfg.Height, err)
	}
	mt, out = fit("image/png", encode(t, photo(400, 300), "png"), 0, true)
	img, _, err := picture.Decode(bytes.NewReader(out))
	if err != nil || mt != "image/png" || img.ColorModel() != color.GrayModel {
		t.Fatalf("grey: %s %v, %v", mt, img.ColorModel(), err)
	}
	if mt, _ := fit("image/gif", encode(t, photo(400, 300), "gif"), 200, false); mt != "image/png" {
		t.Errorf("a GIF shrunk comes back as %s, want a PNG", mt)
	}
}

func second(_ string, b []byte) []byte { return b }

// turned returns a JPEG whose Exif segment, in the byte order given, says the
// reader turns it: orientation 6, a phone held upright.
func turned(t *testing.T, order string) []byte {
	plain := encode(t, photo(2000, 100), "jpeg")
	tiff := []byte(order + "\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	if order == "II" {
		tiff = []byte("II\x2a\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}, seg...)
	return append(append(append([]byte{}, plain[:2]...), app1...), plain[2:]...)
}

func TestFitKeepsTurnedPhotos(t *testing.T) {
	for _, order := range []string{"MM", "II"} {
		in := turned(t, order)
		if _, out := fit("image/jpeg", in, 1000, true); !bytes.Equal(out, in) {
			t.Errorf("%s: a photo the reader turns was re-encoded, losing its turn", order)
		}
	}
	// The same segment saying upright leaves the photo free to fit.
	up := bytes.Replace(turned(t, "MM"), []byte{0x00, 0x06, 0x00, 0x00}, []byte{0x00, 0x01, 0x00, 0x00}, 1)
	if _, out := fit("image/jpeg", up, 1000, true); bytes.Equal(out, up) {
		t.Error("an upright photo was left as it came")
	}
}

func TestFitGreysClearOnWhite(t *testing.T) {
	img := picture.NewNRGBA(picture.Rect(0, 0, 40, 40))
	img.Set(5, 5, color.NRGBA{0, 0, 0, 255})
	_, out := fit("image/png", encode(t, img, "png"), 0, true)
	g, _, err := picture.Decode(bytes.NewReader(out))
	if err != nil || g.At(20, 20) != (color.Gray{255}) || g.At(5, 5) != (color.Gray{0}) {
		t.Fatalf("a clear background greys to %v under a dot of %v, want white under black: %v", g.At(20, 20), g.At(5, 5), err)
	}
}

func TestFitLeavesAlone(t *testing.T) {
	small := encode(t, photo(300, 200), "jpeg")
	if _, out := fit("image/jpeg", small, 1000, false); !bytes.Equal(out, small) {
		t.Error("an image narrower than the screen was re-encoded with nothing asked")
	}
	if _, out := fit("image/jpeg", small, 0, false); !bytes.Equal(out, small) {
		t.Error("no width and no grey changed the image")
	}
	webp := []byte("RIFF....WEBPVP8 ")
	if mt, out := fit("image/webp", webp, 100, true); mt != "image/webp" || !bytes.Equal(out, webp) {
		t.Error("a type it cannot decode was changed")
	}
	edge := encode(t, photo(1000, 10), "jpeg")
	if _, out := fit("image/jpeg", edge, 1000, false); !bytes.Equal(out, edge) {
		t.Error("an image as wide as the screen was re-encoded with nothing asked")
	}
	two := picture.NewPaletted(picture.Rect(0, 0, 400, 300), color.Palette{color.Black, color.White})
	seed := uint32(7)
	for i := range two.Pix {
		seed = seed*1664525 + 1013904223
		two.Pix[i] = uint8(seed>>24) & 1
	}
	if in := encode(t, two, "png"); !bytes.Equal(second(fit("image/png", in, 0, true)), in) {
		t.Error("a grey copy heavier than the picture was kept")
	}
	// A header naming more pixels than maxPixels is never decoded.
	huge := encode(t, picture.NewGray(picture.Rect(0, 0, 6000, 4001)), "png")
	if _, out := fit("image/png", huge, 1000, true); !bytes.Equal(out, huge) {
		t.Error("an image past maxPixels was decoded")
	}
}
