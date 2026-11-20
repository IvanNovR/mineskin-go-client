package mineskin

import (
	"log/slog"
	"time"

	"github.com/IvanNovR/mineskin-go-client/internal/clock"
)

const (
	defaultTimeout          = 10 * time.Second
	defaultRateLimitRetries = 2
)

// defaultConfig returns a config with every default set. Options are applied
// on top of it and validate checks the result once.
func defaultConfig() *config {
	return &config{
		baseURL:          DefaultBaseURL,
		userAgent:        DefaultUserAgent,
		timeout:          defaultTimeout,
		logger:           slog.New(slog.DiscardHandler),
		polling:          PollOptions{}.withDefaults(),
		generate:         generatePacing{throttle: defaultGenerateThrottle()},
		getThrottle:      defaultGetThrottle(),
		rateLimitRetries: defaultRateLimitRetries,
		clock:            clock.Real(),
	}
}

// validate checks the config and stores the normalized base URL. Errors match
// ErrInvalidInput and never contain the API key.
func (cfg *config) validate() error {
	baseURL, err := normalizeBaseURL(cfg.baseURL)
	if err != nil {
		return err
	}
	cfg.baseURL = baseURL
	if cfg.rateLimitRetries < 0 {
		return invalidInput("rate limit retries must not be negative")
	}
	if cfg.timeout <= 0 {
		return invalidInput("timeout must be greater than zero")
	}
	if err := cfg.polling.validate(); err != nil {
		return err
	}
	if err := cfg.generate.throttle.validate("generate"); err != nil {
		return err
	}
	if err := cfg.getThrottle.validate("get"); err != nil {
		return err
	}
	return validateHeaderValues(cfg.userAgent, cfg.apiKey.Reveal())
}
