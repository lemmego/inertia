package inertia

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"regexp"
	"testing"
)

// T is the minimal interface for testing helpers.
type T interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

var _ T = (*testing.T)(nil)

// ---------------------------------------------------------------------------
// Internal page data model (mirrors gonertia's page for parsing)
// ---------------------------------------------------------------------------

type assertablePage struct {
	Component      string                   `json:"component"`
	Props          map[string]any           `json:"props"`
	Flash          Flash                    `json:"flash,omitempty"`
	URL            string                   `json:"url"`
	Version        string                   `json:"version"`
	EncryptHistory bool                     `json:"encryptHistory"`
	ClearHistory   bool                     `json:"clearHistory"`
	DeferredProps  map[string][]string      `json:"deferredProps,omitempty"`
	MergeProps     []string                 `json:"mergeProps,omitempty"`
	PrependProps   []string                 `json:"prependProps,omitempty"`
	DeepMergeProps []string                 `json:"deepMergeProps,omitempty"`
	MatchPropsOn   []string                 `json:"matchPropsOn,omitempty"`
	ScrollProps    map[string]map[string]any `json:"scrollProps,omitempty"`
}

// ---------------------------------------------------------------------------
// Assertable
// ---------------------------------------------------------------------------

// Assertable wraps an Inertia page response with assertion helpers for testing.
type Assertable struct {
	t    T
	page *assertablePage
	Body *bytes.Buffer
}

// Component returns the page component name.
func (a Assertable) Component() string { return a.page.Component }

// Version returns the asset version.
func (a Assertable) Version() string { return a.page.Version }

// URL returns the request URL.
func (a Assertable) URL() string { return a.page.URL }

// Props returns the page props.
func (a Assertable) Props() map[string]any { return a.page.Props }

// EncryptHistory returns the encrypt history flag.
func (a Assertable) EncryptHistory() bool { return a.page.EncryptHistory }

// ClearHistory returns the clear history flag.
func (a Assertable) ClearHistory() bool { return a.page.ClearHistory }

// MergeProps returns the merge props list.
func (a Assertable) MergeProps() []string { return a.page.MergeProps }

// PrependProps returns the prepend props list.
func (a Assertable) PrependProps() []string { return a.page.PrependProps }

// DeepMergeProps returns the deep merge props list.
func (a Assertable) DeepMergeProps() []string { return a.page.DeepMergeProps }

// MatchPropsOn returns the match props on list.
func (a Assertable) MatchPropsOn() []string { return a.page.MatchPropsOn }

// DeferredProps returns the deferred props map.
func (a Assertable) DeferredProps() map[string][]string { return a.page.DeferredProps }

// ScrollProps returns the scroll props metadata as a generic map.
func (a Assertable) ScrollProps() map[string]map[string]any { return a.page.ScrollProps }

// ---------------------------------------------------------------------------
// Assertion methods
// ---------------------------------------------------------------------------

// AssertComponent verifies that the response component matches want.
func (a Assertable) AssertComponent(want string) {
	a.t.Helper()
	if a.page.Component != want {
		a.t.Fatalf("inertia: Component=%s, want=%s", a.page.Component, want)
	}
}

// AssertVersion verifies that the response version matches want.
func (a Assertable) AssertVersion(want string) {
	a.t.Helper()
	if a.page.Version != want {
		a.t.Fatalf("inertia: Version=%s, want=%s", a.page.Version, want)
	}
}

// AssertURL verifies that the response URL matches want.
func (a Assertable) AssertURL(want string) {
	a.t.Helper()
	if a.page.URL != want {
		a.t.Fatalf("inertia: URL=%s, want=%s", a.page.URL, want)
	}
}

// AssertProps verifies that the response props match want.
func (a Assertable) AssertProps(want map[string]any) {
	a.t.Helper()
	if !reflect.DeepEqual(a.page.Props, want) {
		a.t.Fatalf("inertia: Props=%#v, want=%#v", a.page.Props, want)
	}
}

// AssertEncryptHistory verifies the encrypt history flag.
func (a Assertable) AssertEncryptHistory(want bool) {
	a.t.Helper()
	if a.page.EncryptHistory != want {
		a.t.Fatalf("inertia: EncryptHistory=%t, want=%t", a.page.EncryptHistory, want)
	}
}

// AssertClearHistory verifies the clear history flag.
func (a Assertable) AssertClearHistory(want bool) {
	a.t.Helper()
	if a.page.ClearHistory != want {
		a.t.Fatalf("inertia: ClearHistory=%t, want=%t", a.page.ClearHistory, want)
	}
}

// AssertMergeProps verifies the merge props list.
func (a Assertable) AssertMergeProps(want []string) {
	a.t.Helper()
	if !reflect.DeepEqual(a.page.MergeProps, want) {
		a.t.Fatalf("inertia: MergeProps=%#v, want=%#v", a.page.MergeProps, want)
	}
}

// AssertDeferredProps verifies the deferred props map.
func (a Assertable) AssertDeferredProps(want map[string][]string) {
	a.t.Helper()
	if !reflect.DeepEqual(a.page.DeferredProps, want) {
		a.t.Fatalf("inertia: DeferredProps=%#v, want=%#v", a.page.DeferredProps, want)
	}
}

// ---------------------------------------------------------------------------
// Constructors — parse an Inertia response from JSON or HTML
// ---------------------------------------------------------------------------

var dataPageScriptRe = regexp.MustCompile(`(?s)<script[^>]* data-page="[^"]*"[^>]*>(.*?)</script>`)

// AssertFromReader parses an Inertia response body and returns an Assertable.
func AssertFromReader(t T, body io.Reader) Assertable {
	t.Helper()
	bs, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("inertia: read body: %s", err)
	}
	return AssertFromBytes(t, bs)
}

// AssertFromString parses an Inertia response string and returns an Assertable.
func AssertFromString(t T, body string) Assertable {
	t.Helper()
	return AssertFromBytes(t, []byte(body))
}

// AssertFromBytes parses an Inertia response bytes and returns an Assertable.
func AssertFromBytes(t T, body []byte) Assertable {
	t.Helper()

	buf := bytes.NewBuffer(body)
	p := &assertablePage{}

	// Try JSON first (Inertia JSON response).
	if err := json.Unmarshal(buf.Bytes(), p); err == nil {
		return Assertable{t: t, page: p, Body: buf}
	}

	// Fall back to HTML: locate the data-page script tag.
	matches := dataPageScriptRe.FindAllStringSubmatch(buf.String(), -1)
	for _, m := range matches {
		if len(m) > 1 {
			if err := json.Unmarshal([]byte(m[1]), p); err == nil {
				return Assertable{t: t, page: p, Body: buf}
			}
		}
	}

	t.Fatal("inertia: invalid inertia response — no page data found")
	return Assertable{t: t, page: p, Body: buf}
}
