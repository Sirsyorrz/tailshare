package thumb

import (
	"encoding/binary"
	"image"
	"io"
)

// exifOrientation reads the EXIF orientation tag from a JPEG, 1 if absent.
// Phones store portrait photos sideways and set this tag instead of
// rotating the pixels.
func exifOrientation(r io.Reader) int {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil || hdr != [2]byte{0xFF, 0xD8} {
		return 1
	}
	for {
		var m [4]byte
		if _, err := io.ReadFull(r, m[:]); err != nil || m[0] != 0xFF {
			return 1
		}
		marker, n := m[1], int(binary.BigEndian.Uint16(m[2:]))-2
		if n < 0 {
			return 1
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		seg := make([]byte, n)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 1
		}
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return parseTIFFOrientation(seg[6:])
		}
	}
}

func parseTIFFOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off:]))
	for i := range count {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation (1-8) to img.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
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
