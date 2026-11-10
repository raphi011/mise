package ai

import (
	"context"
	"net/url"
	"time"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/store"
)

// PublicAddr exposes the address check.
var PublicAddr = publicAddr

// PageDraft exposes the no-model import of a page.
func (s *Service) PageDraft(ctx context.Context, sc store.Scoped, rawURL string, lang language.Tag) (Draft, Page, error) {
	return s.pageDraft(ctx, sc, rawURL, lang)
}

// memProgress records steps in memory.
type memProgress struct{ steps []JobStep }

func (p *memProgress) start(s JobStep) int {
	p.steps = append(p.steps, s)
	return len(p.steps) - 1
}

func (p *memProgress) finish(i int, state StepState) { p.steps[i].State = state }

// LoopInput mirrors loopInput for tests.
type LoopInput struct {
	SourceURL   string
	Title, Text string
	Lang        language.Tag
	Perms       Permissions
}

// RunLoop exposes the tool loop with steps recorded in memory.
func (s *Service) RunLoop(ctx context.Context, sc store.Scoped, m Model, in LoopInput) (Draft, []JobStep, error) {
	li := loopInput{Title: in.Title, Text: in.Text, Lang: in.Lang, Perms: in.Perms}
	if in.SourceURL != "" {
		u, err := url.Parse(in.SourceURL)
		if err != nil {
			return Draft{}, nil, err
		}
		li.SourceURL = u
	}
	p := &memProgress{}
	d, err := s.runLoop(ctx, sc, m, li, p)
	return d, p.steps, err
}

// MaxToolResult exposes the tool result cap.
const MaxToolResult = maxToolResult

// SetJobTimeout shortens the per-job limit.
func (s *Service) SetJobTimeout(d time.Duration) { s.jobTimeout = d }
