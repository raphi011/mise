package images

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Red top-left pixel block to check orientation.
	for y := range h {
		for x := range w {
			c := color.RGBA{0, 0, 255, 255}
			if x < w/4 && y < h/4 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withOrientation inserts an EXIF APP1 segment with the orientation after
// the JPEG's SOI marker.
func withOrientation(b []byte, o uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")  // big endian, IFD at 8
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, o)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0) // value padding, next IFD
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)
	out := append([]byte{}, b[:2]...)
	out = append(out, app1...)
	return append(out, b[2:]...)
}

func decode(t *testing.T, s *Store, hh uuid.UUID, hash string, size Size) image.Image {
	t.Helper()
	f, err := s.Open(hh, hash, size)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestSaveResizesAndOrients(t *testing.T) {
	s := New(t.TempDir())
	hh := uuid.New()
	hash, err := s.Save(t.Context(), hh, bytes.NewReader(withOrientation(encodeJPEG(t, 3000, 1000), 6)))
	if err != nil {
		t.Fatal(err)
	}
	// Rotated 90° clockwise: portrait, and the red corner is now top-right.
	full := decode(t, s, hh, hash, Full)
	if b := full.Bounds(); b.Dx() != 800 || b.Dy() != 2400 {
		t.Errorf("full %v", b)
	}
	if r, _, bl, _ := full.At(790, 10).RGBA(); r < 0xc000 || bl > 0x4000 {
		t.Errorf("top-right not red after orientation")
	}
	if b := decode(t, s, hh, hash, Thumb).Bounds(); b.Dx() != 200 || b.Dy() != 600 {
		t.Errorf("thumb %v", b)
	}

	// The same upload is stored once, under the same hash.
	again, err := s.Save(t.Context(), hh, bytes.NewReader(withOrientation(encodeJPEG(t, 3000, 1000), 6)))
	if err != nil || again != hash {
		t.Errorf("second save %q, %v", again, err)
	}
	list, err := s.List(hh)
	if err != nil || len(list) != 1 || list[0].Hash != hash {
		t.Errorf("list %+v, %v", list, err)
	}
	if hhs, _ := s.Households(); len(hhs) != 1 || hhs[0] != hh {
		t.Errorf("households %v", hhs)
	}
	// Another household cannot open it.
	if _, err := s.Open(uuid.New(), hash, Full); !errors.Is(err, ErrNotFound) {
		t.Errorf("open from other household: %v", err)
	}
	if err := s.Remove(hh, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(hh, hash, Thumb); !errors.Is(err, ErrNotFound) {
		t.Errorf("open removed: %v", err)
	}
}

func TestSmallPNGKeepsSize(t *testing.T) {
	s := New(t.TempDir())
	hh := uuid.New()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 300, 200))); err != nil {
		t.Fatal(err)
	}
	hash, err := s.Save(t.Context(), hh, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if b := decode(t, s, hh, hash, Full).Bounds(); b.Dx() != 300 || b.Dy() != 200 {
		t.Errorf("full %v", b)
	}
}

// pngHeader is a PNG signature and IHDR claiming the dimensions, enough
// for DecodeConfig.
func pngHeader(w, h uint32) []byte { return pngHeaderDepth(w, h, 8) }

// pngHeaderDepth is the start of an RGBA PNG with depth bits per sample.
func pngHeaderDepth(w, h uint32, depth byte) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, depth, 6, 0, 0, 0)
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

func TestRejects(t *testing.T) {
	s := New(t.TempDir())
	hh := uuid.New()
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for name, tc := range map[string]struct {
		data []byte
		want error
	}{
		"heic":       {heic, ErrHEIC},
		"text":       {[]byte("hello, not an image"), ErrUnsupported},
		"gif":        {[]byte("GIF89a\x01\x00\x01\x00"), ErrUnsupported},
		"huge":       {pngHeader(8000, 6000), ErrTooManyPx},
		"16-bit":     {pngHeaderDepth(6000, 5000, 16), ErrTooManyPx},
		"broken png": {pngHeader(10, 10), ErrUnsupported},
		"too large":  {make([]byte, MaxUploadBytes+1), ErrTooLarge},
	} {
		if _, err := s.Save(t.Context(), hh, bytes.NewReader(tc.data)); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if _, err := s.Open(hh, "../../etc/passwd", Full); !errors.Is(err, ErrNotFound) {
		t.Errorf("path traversal: %v", err)
	}
}

func TestExifOrientationMalformed(t *testing.T) {
	for _, b := range [][]byte{
		{0xFF, 0xD8},
		{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF},
		append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10}, []byte("Exif\x00\x00MM\x00\x2a\xff\xff\xff\xff")...),
	} {
		if got := exifOrientation(b); got != 1 {
			t.Errorf("exifOrientation(%x) = %d", b, got)
		}
	}
}

// TestSaveStoredAndSweepable checks the "already stored" shortcut: it needs
// both sizes, and it marks them as fresh for the orphan sweep.
func TestSaveStoredAndSweepable(t *testing.T) {
	s := New(t.TempDir())
	hh := uuid.New()
	data := encodeJPEG(t, 800, 600)
	hash, err := s.Save(t.Context(), hh, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// A save that lost its full size is redone, not taken as stored.
	if err := os.Remove(s.path(hh, hash, Full)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(t.Context(), hh, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if f, err := s.Open(hh, hash, Full); err != nil {
		t.Fatalf("full size not rewritten: %v", err)
	} else {
		_ = f.Close()
	}

	// Saving again refreshes the files' time.
	old := time.Now().Add(-48 * time.Hour)
	for _, size := range []Size{Full, Thumb} {
		if err := os.Chtimes(s.path(hh, hash, size), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Save(t.Context(), hh, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(hh)
	if err != nil || len(list) != 1 || !list[0].ModTime.After(old.Add(time.Hour)) {
		t.Fatalf("list after re-save: %+v, %v", list, err)
	}

	// A full size without its thumbnail is still listed, so the sweep
	// can remove it.
	if err := os.Remove(s.path(hh, hash, Thumb)); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List(hh); err != nil || len(list) != 1 || list[0].Hash != hash {
		t.Errorf("orphan full size not listed: %+v, %v", list, err)
	}
}
