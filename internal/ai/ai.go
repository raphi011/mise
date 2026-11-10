package ai

import (
	"errors"
	"log/slog"
	"time"

	"github.com/raphi011/mise/internal/groceries"
	"github.com/raphi011/mise/internal/recipes"
	"github.com/raphi011/mise/internal/store"
)

// ProviderFunc builds a Model for a connection.
type ProviderFunc func(Connection) (Model, error)

// errNoProvider is returned for a provider without a client.
var errNoProvider = errors.New("ai: no client for the provider")

// Service implements recipe import on top of the store.
type Service struct {
	groceries   *groceries.Service
	recipes     *recipes.Service
	fetcher     *Fetcher
	box         *Box
	now         func() time.Time
	newProvider ProviderFunc
	db          *store.DB
	pub         UserPublisher
	log         *slog.Logger
	run         *runner
	jobTimeout  time.Duration
	// started is when this process's service was built: queued and
	// running jobs older than that are a previous process's leftovers.
	started time.Time
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
	// DB runs import jobs across households (Run); Publisher tells the
	// importer's pages about progress. Both may be nil in tests that do
	// not run jobs.
	DB        *store.DB
	Publisher UserPublisher
	// Log defaults to discarding.
	Log *slog.Logger
	// Provider builds model clients; nil means Clients{}.New. Tests pass
	// fakes.
	Provider ProviderFunc
}

// New returns a Service.
func New(cfg Config) *Service {
	s := &Service{box: cfg.Box, groceries: cfg.Groceries, recipes: cfg.Recipes, fetcher: cfg.Fetcher, now: cfg.Now,
		newProvider: cfg.Provider, db: cfg.DB, pub: cfg.Publisher, log: cfg.Log, run: newRunner(), jobTimeout: JobTimeout}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.fetcher == nil {
		s.fetcher = NewFetcher()
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.started = s.now()
	if s.newProvider == nil {
		s.newProvider = Clients{}.New
	}
	return s
}
