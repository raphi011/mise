package ai

import (
	"context"
	"errors"
	"time"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/recipes"
)

// Model is a chat model behind a provider API. The provider clients come
// with the tool loop; until then only the connection test uses it.
type Model interface {
	// Test sends one tiny request and reports whether the model answered.
	Test(ctx context.Context) error
}

// ProviderFunc builds a Model for a connection.
type ProviderFunc func(Connection) (Model, error)

// errNoProvider is returned when no provider clients are configured.
var errNoProvider = errors.New("ai: no provider clients")

// Service implements recipe import on top of the store.
type Service struct {
	groceries   *groceries.Service
	recipes     *recipes.Service
	fetcher     *Fetcher
	box         *Box
	now         func() time.Time
	newProvider ProviderFunc
}

// Config configures a Service.
type Config struct {
	// Box seals API keys. Required.
	Box *Box
	// Groceries and Recipes match ingredients and tags and save recipes.
	Groceries *groceries.Service
	Recipes   *recipes.Service
	// Fetcher fetches pages and photos; nil means NewFetcher.
	Fetcher *Fetcher
	// Now defaults to time.Now.
	Now func() time.Time
	// Provider builds model clients; tests pass fakes.
	Provider ProviderFunc
}

// New returns a Service.
func New(cfg Config) *Service {
	s := &Service{box: cfg.Box, groceries: cfg.Groceries, recipes: cfg.Recipes, fetcher: cfg.Fetcher, now: cfg.Now, newProvider: cfg.Provider}
	if s.fetcher == nil {
		s.fetcher = NewFetcher()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newProvider == nil {
		s.newProvider = func(Connection) (Model, error) { return nil, errNoProvider }
	}
	return s
}
