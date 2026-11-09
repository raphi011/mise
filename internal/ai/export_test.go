package ai

import (
	"context"
	"net/netip"
	"time"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
)

// PublicAddr exposes the address check.
var PublicAddr = publicAddr

// AllowForTest lets f dial addresses allowed by fn (tests serve from
// loopback on random ports) and sets a shorter timeout when non-zero.
func (f *Fetcher) AllowForTest(fn func(netip.AddrPort) bool, timeout time.Duration) *Fetcher {
	f.allowed = fn
	if timeout > 0 {
		f.timeout = timeout
	}
	return f
}

// PageDraft exposes the no-model import of a page.
func (s *Service) PageDraft(ctx context.Context, sc store.Scoped, rawURL string, lang language.Tag) (Draft, Page, error) {
	return s.pageDraft(ctx, sc, rawURL, lang)
}
