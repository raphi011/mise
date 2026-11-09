// Package images stores recipe photos: it validates uploads, applies EXIF
// orientation, re-encodes them as JPEG (which strips all metadata) in two
// sizes and serves them by content hash. Files live under
// <data dir>/images/<household id>/<hash>-<size>.jpg (docs/plan.md, Images).
package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register decoder
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register decoder
)

// Limits.
const (
	// MaxUploadBytes caps an upload's size.
	MaxUploadBytes = 15 << 20
	// MaxPixels caps an upload's dimensions before it is decoded.
	MaxPixels = 40_000_000
	// maxDecodeBytes caps the memory a decoded upload may take: 40
	// megapixels at 8-bit RGBA. A 16-bit image of that size would take
	// twice as much and is rejected.
	maxDecodeBytes = 4 * MaxPixels
	// FullEdge and ThumbEdge are the long edges of the stored sizes.
	FullEdge  = 2400
	ThumbEdge = 600
	quality   = 85
)

// Size names a stored size.
type Size string

// Sizes.
const (
	Full  Size = "full"
	Thumb Size = "thumb"
)

// Upload errors; the web layer shows each with its own message.
var (
	ErrTooLarge    = errors.New("images: file too large")
	ErrTooManyPx   = errors.New("images: image has too many pixels")
	ErrHEIC        = errors.New("images: HEIC/HEIF is not supported")
	ErrUnsupported = errors.New("images: not a JPEG, PNG or WebP image")
	ErrNotFound    = errors.New("images: not found")
)

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash reports whether s looks like a stored image's hash.
func ValidHash(s string) bool { return hashRE.MatchString(s) }

// Store saves and opens images under a directory.
type Store struct {
	dir string
	// sem bounds concurrent decodes: each takes up to maxDecodeBytes.
	sem chan struct{}
}

// New returns a Store under dataDir/images.
func New(dataDir string) *Store {
	return &Store{dir: filepath.Join(dataDir, "images"), sem: make(chan struct{}, 2)}
}

func (s *Store) householdDir(hh uuid.UUID) string { return filepath.Join(s.dir, hh.String()) }

func (s *Store) path(hh uuid.UUID, hash string, size Size) string {
	return filepath.Join(s.householdDir(hh), hash+"-"+string(size)+".jpg")
}

// Save validates an upload, stores both sizes for the household and
// returns the content hash of the upload, which recipes reference. Saving
// the same file twice stores it once.
func (s *Store) Save(ctx context.Context, hh uuid.UUID, r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxUploadBytes {
		return "", ErrTooLarge
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if stored, err := s.touch(hh, hash); err != nil || stored {
		return hash, err
	}

	switch {
	case isHEIF(data):
		return "", ErrHEIC
	case !isJPEG(data) && !isPNG(data) && !isWebP(data):
		return "", ErrUnsupported
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", ErrUnsupported
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if cfg.Width <= 0 || cfg.Height <= 0 || px > MaxPixels || px*bytesPerPixel(cfg.ColorModel) > maxDecodeBytes {
		return "", ErrTooManyPx
	}

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.sem }()

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", ErrUnsupported
	}
	orient := 1
	if isJPEG(data) {
		orient = exifOrientation(data)
	}
	full := orientImage(resize(src, FullEdge), orient)
	thumb := resize(full, ThumbEdge)

	if err := os.MkdirAll(s.householdDir(hh), 0o750); err != nil {
		return "", err
	}
	// Full first: a thumbnail marks the image as stored (see touch and
	// List), so it must not exist without the full size.
	if err := writeJPEG(s.path(hh, hash, Full), full); err != nil {
		return "", err
	}
	if err := writeJPEG(s.path(hh, hash, Thumb), thumb); err != nil {
		return "", err
	}
	return hash, nil
}

// touch reports whether both sizes of hash are stored and, if so, marks
// them as just written: the orphan sweep only deletes images unreferenced
// for a while, and one uploaded again is about to be referenced.
func (s *Store) touch(hh uuid.UUID, hash string) (bool, error) {
	now := time.Now()
	for _, size := range []Size{Full, Thumb} {
		err := os.Chtimes(s.path(hh, hash, size), now, now)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

// bytesPerPixel is roughly what decoding an image of model m takes per
// pixel.
func bytesPerPixel(m color.Model) int64 {
	switch m {
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	case color.Gray16Model:
		return 2
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.YCbCrModel, color.CMYKModel:
		return 4
	}
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	return 4
}

// Open opens a stored image of the household.
func (s *Store) Open(hh uuid.UUID, hash string, size Size) (*os.File, error) {
	if !ValidHash(hash) || (size != Full && size != Thumb) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.path(hh, hash, size))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Stored is one stored image of a household.
type Stored struct {
	Hash    string
	ModTime time.Time
}

// Households lists the households that have an image directory.
func (s *Store) Households() ([]uuid.UUID, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, e := range entries {
		if id, err := uuid.Parse(e.Name()); err == nil && e.IsDir() {
			out = append(out, id)
		}
	}
	return out, nil
}

// List returns the images stored for a household, one per hash, with the
// time its newest file was written. A full size left without its
// thumbnail by a failed save is listed too, so the sweep removes it.
func (s *Store) List(hh uuid.UUID) ([]Stored, error) {
	entries, err := os.ReadDir(s.householdDir(hh))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Stored
	seen := map[string]int{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jpg")
		if !ok {
			continue
		}
		hash, size, ok := strings.Cut(name, "-")
		if !ok || !ValidHash(hash) || (Size(size) != Full && Size(size) != Thumb) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if i, ok := seen[hash]; ok {
			if info.ModTime().After(out[i].ModTime) {
				out[i].ModTime = info.ModTime()
			}
			continue
		}
		seen[hash] = len(out)
		out = append(out, Stored{Hash: hash, ModTime: info.ModTime()})
	}
	return out, nil
}

// Remove deletes both sizes of an image.
func (s *Store) Remove(hh uuid.UUID, hash string) error {
	if !ValidHash(hash) {
		return ErrNotFound
	}
	var errs []error
	for _, size := range []Size{Full, Thumb} {
		if err := os.Remove(s.path(hh, hash, size)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeJPEG(path string, img image.Image) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := jpeg.Encode(tmp, img, &jpeg.Options{Quality: quality}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("images: encode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// resize scales img down so its long edge is at most edge.
func resize(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		return img
	}
	if w >= h {
		h = max(1, h*edge/w)
		w = edge
	} else {
		w = max(1, w*edge/h)
		h = edge
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF }

func isPNG(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }

func isWebP(b []byte) bool {
	return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

// isHEIF reports an ISO base media file whose brand is HEIF/HEIC or AVIF,
// which the image packages cannot decode. iPhones upload HEIC from Files.
func isHEIF(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	switch string(b[8:12]) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif", "avis":
		return true
	}
	return false
}
