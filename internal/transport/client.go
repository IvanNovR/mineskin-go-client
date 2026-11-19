package transport

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
)

// MaxRedirects is the number of redirects after which CheckRedirect stops.
const MaxRedirects = 10

// CheckRedirect refuses redirects from https to http and stops after
// MaxRedirects redirects. It is an http.Client.CheckRedirect function; its
// errors are raw and the caller adds its own context.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme == "http" {
		for _, prev := range via {
			if prev.URL.Scheme == "https" {
				return errors.New("refusing redirect from https to http")
			}
		}
	}
	return nil
}

// DefaultTransport returns a private clone of http.DefaultTransport, which the
// caller owns and closes. If the default was replaced by another RoundTripper,
// a fresh transport with the standard library defaults is used.
func DefaultTransport() *http.Transport {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		return t.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

// HasControlChar reports whether s contains a control character.
func HasControlChar(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}
