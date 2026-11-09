package ai

import (
	"net/http"
	"time"
)

// Clients builds the provider clients. The zero value talks to the real
// APIs.
type Clients struct {
	// HTTPClient defaults to one with a 3-minute timeout, the job limit.
	// It is not the safe fetcher: the endpoint is configured by an admin
	// and often on the local network (Ollama).
	HTTPClient *http.Client
	// AnthropicURL overrides the Anthropic API's base URL (tests).
	AnthropicURL string
}

// New returns a Model for c.
func (cl Clients) New(c Connection) (Model, error) {
	hc := cl.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Minute}
	}
	switch c.Provider {
	case ProviderAnthropic:
		return newAnthropic(c, cl.AnthropicURL, hc), nil
	case ProviderOpenAI, ProviderOllama:
		return newOpenAI(c, hc), nil
	}
	return nil, errNoProvider
}
