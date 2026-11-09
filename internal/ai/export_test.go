package ai

import (
	"net/netip"
	"time"
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
