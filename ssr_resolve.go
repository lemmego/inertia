package inertia

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

// ssrResolveTTL bounds how often the hot file is consulted.
//
// One stat per second per process, against a render path that already makes an
// HTTP request to Node. Long enough that it is free, short enough that a dev
// server coming up is picked up on the next page load rather than the next
// restart.
const ssrResolveTTL = time.Second

// standaloneSSRURL is where @inertiajs/react's own server listens by default,
// and so where a render goes when no Vite dev server has published itself.
const standaloneSSRURL = "http://127.0.0.1:13714"

// ssrRedirector rewrites each server-render request to wherever the renderer
// is *now*, rather than wherever it was when the process booted.
//
// detectSSRURL used to be called once, in NewInertiaWithError, and that made a
// fresh Inertia project answer 500 on every page under `lemmego dev`. The
// chain: strict SSR is on outside production, so a failed render is an error
// rather than a silent degrade; `lemmego dev` deliberately does not start the
// standalone renderer, because @inertiajs/vite serves /__inertia_ssr from the
// Vite dev server and a second renderer would be wrong; and the Vite URL is
// published by writing ./public/hot. But dev starts air before vite, so on
// first run the hot file does not exist yet, detectSSRURL returned the
// standalone fallback at 127.0.0.1:13714, nothing was listening there, and the
// URL was then fixed for the life of the process — so every page stayed a 500
// until something happened to restart the binary.
//
// Resolving per render closes it. gonertia takes its SSR URL as a string at
// construction and we do not control that, but the HTTP client is ours, so the
// address is decided in the transport where it can still change.
//
// In production there is no hot file, so this resolves to the configured
// standalone URL every time and costs one cached stat.
type ssrRedirector struct {
	base http.RoundTripper

	mu       sync.Mutex
	resolved string
	at       time.Time
}

func newSSRTransport(base http.RoundTripper) *ssrRedirector {
	if base == nil {
		base = http.DefaultTransport
	}
	return &ssrRedirector{base: base}
}

func (s *ssrRedirector) RoundTrip(req *http.Request) (*http.Response, error) {
	target := s.current()
	if target == "" {
		return s.base.RoundTrip(req)
	}

	parsed, err := url.Parse(target)
	if err != nil {
		// An unreadable hot file is not a reason to fail the render: let it
		// go to whatever gonertia was configured with, and let the SSR guard
		// report the degrade if that address is dead too.
		return s.base.RoundTrip(req)
	}

	// A shallow copy, because a RoundTripper must not modify the request it
	// is given — the same *http.Request may be retried or inspected by the
	// caller afterwards.
	clone := req.Clone(req.Context())
	clone.URL.Scheme = parsed.Scheme
	clone.URL.Host = parsed.Host
	clone.URL.Path = parsed.Path
	clone.Host = parsed.Host
	return s.base.RoundTrip(clone)
}

// current returns the renderer address, re-reading the hot file at most once
// per ssrResolveTTL.
func (s *ssrRedirector) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resolved != "" && time.Since(s.at) < ssrResolveTTL {
		return s.resolved
	}
	s.resolved = detectSSRURL()
	s.at = time.Now()
	return s.resolved
}
