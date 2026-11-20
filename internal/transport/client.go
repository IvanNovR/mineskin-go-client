package transport

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
)

// MaxRedirects is the number of redirects after which CheckRedirect stops.
const MaxRedirects = 10

// CheckRedirect refuses redirects from https to http, stops after
// MaxRedirects redirects and removes the Authorization header from a redirect
// to another host (a different port or a sibling subdomain counts too; the
// standard library compares the host name only). The API never redirects. It is an http.Client.CheckRedirect function; its
// errors are raw and the caller adds its own context.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
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
