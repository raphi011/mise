package ai

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/text/language"

	"github.com/raphi011/mise/internal/i18n"
	"github.com/raphi011/mise/internal/store"
)

// ErrNoRecipe means the page has no schema.org Recipe data.
var ErrNoRecipe = errors.New("ai: no recipe data on the page")

// ErrUnreachable wraps why a page could not be fetched.
var ErrUnreachable = errors.New("ai: page could not be fetched")

// pageDraft fetches a page and builds a draft from its JSON-LD without a
// model. Without JSON-LD it returns the parsed page and ErrNoRecipe, so
// the caller can hand the text to the model. lang is the importer's
// language, used for amounts when the page does not declare one.
func (s *Service) pageDraft(ctx context.Context, sc store.Scoped, rawURL string, lang language.Tag) (Draft, Page, error) {
	res, err := s.fetcher.Fetch(ctx, rawURL, KindPage)
	if err != nil {
		return Draft{}, Page{}, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	page := ParsePage(res)
	if page.Recipe == nil {
		return Draft{}, page, ErrNoRecipe
	}
	d, err := s.draftFromPage(ctx, sc, page, lang)
	return d, page, err
}

// PreviewDraft builds the draft importing rawURL would start from, from
// the page's recipe data and without a model or an import job, so a
// recipe can be looked at before it is imported. It returns ErrNoRecipe
// for a page without recipe data and ErrUnreachable (wrapping the cause)
// when the page could not be fetched.
func (s *Service) PreviewDraft(ctx context.Context, sc store.Scoped, rawURL string, lang language.Tag) (Draft, error) {
	d, _, err := s.pageDraft(ctx, sc, rawURL, lang)
	return d, err
}

// draftFromPage builds a draft from a page's recipe data (JSON-LD or
// microdata).
func (s *Service) draftFromPage(ctx context.Context, sc store.Scoped, page Page, lang language.Tag) (Draft, error) {
	r := *page.Recipe
	if r.SourceURL == "" && page.URL != nil {
		r.SourceURL = page.URL.String()
	}
	if r.ImageURL == "" {
		r.ImageURL = page.Image
	}
	return s.draftFromRecipe(ctx, sc, r, amountLang(page.Lang, lang))
}

// amountLang picks the language amounts are parsed in: the page's when it
// is one the parser knows, else the importer's.
func amountLang(page, importer language.Tag) language.Tag {
	pb, _ := page.Base()
	for _, tag := range i18n.Supported {
		if b, _ := tag.Base(); page != language.Und && b == pb {
			return tag
		}
	}
	return importer
}
