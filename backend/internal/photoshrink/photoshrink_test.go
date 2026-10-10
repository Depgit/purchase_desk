package photoshrink

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"
)

func noisyJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Intn(256))
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFitShrinksABigPhoto(t *testing.T) {
	data := noisyJPEG(t, 4000, 3000)
	if len(data) <= 5<<20 {
		t.Fatalf("test photo is only %d bytes; expected over 5 MB", len(data))
	}
	out, name, ctype, err := Fit(data, "DSC_0132.JPG", "image/jpeg", 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 5<<20 || name != "DSC_0132.jpg" || ctype != "image/jpeg" {
		t.Fatalf("got %d bytes, %s, %s", len(out), name, ctype)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() > 2400 || b.Dy() > 2400 || b.Dx() < b.Dy() {
		t.Fatalf("size %v: want landscape, longer side at most 2400", b)
	}
}

func TestFitLeavesASmallFileAlone(t *testing.T) {
	data := []byte("%PDF-1.4 small")
	out, name, _, err := Fit(data, "invoice.pdf", "application/pdf", 5<<20)
	if err != nil || !bytes.Equal(out, data) || name != "invoice.pdf" {
		t.Fatalf("small file changed: %v %s", err, name)
	}
}

func TestFitRefusesABigPDF(t *testing.T) {
	if _, _, _, err := Fit(make([]byte, 6<<20), "scan.pdf", "application/pdf", 5<<20); err == nil {
		t.Fatal("a big PDF should be refused")
	}
}

// A phone held upright saves a landscape image marked "rotate 90 degrees" (EXIF orientation 6).
func TestFitTurnsAPhoneShotUpright(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 100)) // stored landscape
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	seg := append([]byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}, app1...)
	data := append(append([]byte{0xFF, 0xD8}, seg...), b.Bytes()[2:]...)
	if o := orientation(data); o != 6 {
		t.Fatalf("orientation %d, want 6", o)
	}
	out, _, _, err := Fit(data, "p.jpg", "image/jpeg", 100) // a tiny limit forces the rework
	if err == nil {
		t.Fatal("expected the 100-byte limit to be impossible")
	}
	out, _, _, err = Fit(data, "p.jpg", "image/jpeg", len(data)-1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := jpeg.Decode(bytes.NewReader(out))
	if bb := got.Bounds(); bb.Dx() != 100 || bb.Dy() != 300 {
		t.Fatalf("size %v, want 100x300 (turned upright)", bb)
	}
}
