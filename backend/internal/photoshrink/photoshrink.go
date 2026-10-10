// Package photoshrink makes a photo small enough for a size limit: it turns it the right way up (from the camera's
// EXIF orientation), scales it down and saves it as JPEG, keeping text such as an invoice readable.
package photoshrink

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // PNG photos and scans decode too
	"strings"
)

// Fit returns data unchanged when it is within maxBytes. A JPEG or PNG over the limit is scaled down until it fits
// and comes back as JPEG with its name ending in .jpg. Anything else over the limit is an error.
func Fit(data []byte, name, contentType string, maxBytes int) ([]byte, string, string, error) {
	if len(data) <= maxBytes {
		return data, name, contentType, nil
	}
	if contentType != "image/jpeg" && contentType != "image/png" {
		return nil, "", "", fmt.Errorf("%s is %.1f MB and cannot be made smaller here", name, mb(len(data)))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", fmt.Errorf("%s could not be read as a photo: %v", name, err)
	}
	o := 1
	if contentType == "image/jpeg" {
		o = orientation(data)
	}
	out := name
	if i := strings.LastIndex(out, "."); i > 0 {
		out = out[:i]
	}
	out += ".jpg"
	for _, side := range []int{2400, 2000, 1600, 1280, 1024} {
		small := upright(scale(img, side), o) // turned after scaling: the same result, far less work
		for _, q := range []int{82, 70} {
			var b bytes.Buffer
			if err := jpeg.Encode(&b, small, &jpeg.Options{Quality: q}); err != nil {
				return nil, "", "", err
			}
			if b.Len() <= maxBytes {
				return b.Bytes(), out, "image/jpeg", nil
			}
		}
	}
	return nil, "", "", fmt.Errorf("%s stays above %.0f MB even when made smaller", name, mb(maxBytes))
}

func mb(n int) float64 { return float64(n) / (1 << 20) }

// scale shrinks img so its longer side is at most side, averaging the pixels each new pixel covers.
func scale(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	nw, nh := side, h*side/w
	if h > w {
		nw, nh = w*side/h, side
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, n uint64
			// Sample at most a 4x4 grid of each covered block: plenty for a smooth result, and fast on big photos.
			sy, sx := max(1, (y1-y0)/4), max(1, (x1-x0)/4)
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, _ := img.At(xx, yy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			dst.Set(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	return dst
}

// orientation reads the EXIF orientation of a JPEG (1 = upright; 3, 6 and 8 are the usual phone rotations).
func orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker, size := data[i+1], int(binary.BigEndian.Uint16(data[i+2:]))
		if marker == 0xDA || size < 2 { // image data begins: no EXIF before it
			return 1
		}
		seg := data[i+4 : min(len(data), i+2+size)]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder = binary.BigEndian
	if string(t[:2]) == "II" {
		bo = binary.LittleEndian
	}
	off := int(bo.Uint32(t[4:]))
	if off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for e := 0; e < n; e++ {
		p := off + 2 + e*12
		if p+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[p:]) == 0x0112 {
			if v := int(bo.Uint16(t[p+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// upright turns or mirrors img as its EXIF orientation says, so it displays the way the camera was held.
func upright(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
