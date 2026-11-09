package images

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSaveLimitsConcurrentDecodes checks that parallel saves never decode
// more images at once than the limit allows.
func TestSaveLimitsConcurrentDecodes(t *testing.T) {
	s := New(t.TempDir())
	var running, peak atomic.Int32
	s.decoding = func() {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
	}
	hh := uuid.New()
	var wg sync.WaitGroup
	for i := range 6 {
		data := encodeJPEG(t, 100+i, 80) // distinct uploads, so none is already stored
		wg.Go(func() {
			if _, err := s.Save(t.Context(), hh, bytes.NewReader(data)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := peak.Load(); got != maxDecodes {
		t.Errorf("peak concurrent decodes %d, want %d", got, maxDecodes)
	}
}

// TestSaveWaitingGivesUp checks that a save waiting for a decode slot
// returns as soon as its request is gone.
func TestSaveWaitingGivesUp(t *testing.T) {
	s := New(t.TempDir())
	for range maxDecodes {
		s.sem <- struct{}{} // every slot busy
	}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := s.Save(ctx, uuid.New(), bytes.NewReader(encodeJPEG(t, 100, 80)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("returned after %v", d)
	}
}
