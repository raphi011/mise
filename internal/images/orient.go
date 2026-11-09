package images

import (
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
)

// jpegFrame is a JPEG's frame header (SOF segment).
type jpegFrame struct {
	progressive   bool
	width, height int
	// factors are the components' horizontal and vertical sampling
	// factors.
	factors [][2]int
}

var errBadFrame = errors.New("images: malformed JPEG frame header")

// readJPEGFrame reads the frame header of a JPEG.
func readJPEGFrame(b []byte) (jpegFrame, error) {
	i := 2 // after SOI
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return jpegFrame{}, errBadFrame
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return jpegFrame{}, errBadFrame
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return jpegFrame{}, errBadFrame
		}
		seg := b[i+4 : i+2+n]
		// SOF0–SOF15, except DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			return parseFrame(marker, seg)
		}
		i += 2 + n
	}
	return jpegFrame{}, errBadFrame
}

func parseFrame(marker byte, seg []byte) (jpegFrame, error) {
	if len(seg) < 6 {
		return jpegFrame{}, errBadFrame
	}
	f := jpegFrame{
		// SOF2, SOF6, SOF10 and SOF14 are progressive.
		progressive: marker&0x03 == 0x02,
		height:      int(binary.BigEndian.Uint16(seg[1:])),
		width:       int(binary.BigEndian.Uint16(seg[3:])),
	}
	nc := int(seg[5])
	if nc < 1 || len(seg) < 6+3*nc {
		return jpegFrame{}, errBadFrame
	}
	for c := range nc {
		hv := seg[6+3*c+1]
		h, v := int(hv>>4), int(hv&0x0F)
		if h < 1 || h > 4 || v < 1 || v > 4 {
			return jpegFrame{}, errBadFrame
		}
		f.factors = append(f.factors, [2]int{h, v})
	}
	return f, nil
}

// coefficientBytes is what image/jpeg allocates for a progressive frame's
// coefficients: one block of 64 int32s for each 8×8 block of every
// component, over whole MCUs (see progCoeffs in image/jpeg's scan.go).
func (f jpegFrame) coefficientBytes() int64 {
	const blockBytes = 64 * 4
	maxH, maxV := 1, 1
	for _, hv := range f.factors {
		maxH, maxV = max(maxH, hv[0]), max(maxV, hv[1])
	}
	mxx := int64((f.width + 8*maxH - 1) / (8 * maxH))
	myy := int64((f.height + 8*maxV - 1) / (8 * maxV))
	var n int64
	for _, hv := range f.factors {
		n += mxx * myy * int64(hv[0]*hv[1]) * blockBytes
	}
	return n
}

// exifOrientation reads the EXIF orientation (1–8) of a JPEG, or 1 when it
// has none or the data is malformed.
func exifOrientation(b []byte) int {
	i := 2 // after SOI
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return 1
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[off:]))
	for k := range count {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 { // Orientation, type SHORT
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orientImage returns img transformed so it displays upright for an EXIF
// orientation.
func orientImage(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)

	dw, dh := w, h
	if o >= 5 { // 5–8 swap width and height
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 clockwise
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 counter-clockwise
				dx, dy = y, w-1-x
			}
			i, j := src.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[j:j+4], src.Pix[i:i+4])
		}
	}
	return dst
}
