package ai

import (
	"context"
	"errors"
	"time"
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
	box         *Box
	now         func() time.Time
	newProvider ProviderFunc
}

// Config configures a Service.
type Config struct {
	// Box seals API keys. Required.
	Box *Box
	// Now defaults to time.Now.
	Now func() time.Time
	// Provider builds model clients; tests pass fakes.
	Provider ProviderFunc
}

// New returns a Service.
func New(cfg Config) *Service {
	s := &Service{box: cfg.Box, now: cfg.Now, newProvider: cfg.Provider}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newProvider == nil {
		s.newProvider = func(Connection) (Model, error) { return nil, errNoProvider }
	}
	return s
}
