package inertia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// ssrFailures counts server-side renders that fell back to client rendering.
//
// Counting failures rather than successes is a consequence of where the seam
// is: gonertia's render path is internal, and the one thing it exposes is a
// log line on failure. That turns out to be the signal worth having anyway —
// a failed render is always a problem, whereas a count of successes only
// means something next to a total.
//
// It matters because the failure is invisible at the HTTP layer. gonertia
// falls back to the client-rendered container, so the response is 200 with an
// empty root element: correct-looking to a person, empty to a crawler. A
// status-code alert will never fire. This will.
var ssrFailures atomic.Uint64

// SSRFailures returns how many server-side renders have fallen back since the
// process started.
//
// Expose it on a readiness endpoint and alert on the rate. A non-zero and
// rising count means every page is being served without server-rendered
// markup, and nothing else in the stack will tell you.
func SSRFailures() uint64 { return ssrFailures.Load() }

// noteSSRFailure is called from the logger bridge, which is where gonertia
// reports a failed render.
func noteSSRFailure() { ssrFailures.Add(1) }

// SSRHealth is what a readiness check reports.
type SSRHealth struct {
	// Enabled is whether this process is configured to server-render at all.
	Enabled bool
	// URL is the render endpoint.
	URL string
	// OK is whether the render endpoint answered a probe.
	OK bool
	// Failures is the process's cumulative fallback count.
	Failures uint64
	// Err describes why a probe failed.
	Err string
}

// CheckSSR probes the render endpoint.
//
// It POSTs a minimal page and requires a non-empty body back, rather than
// dialling the port. A Node process that is listening with a broken module
// graph passes a TCP check and fails every render, which is precisely the
// state this is meant to catch — the CLI's own health check dials the port
// and so cannot see it.
func (i *Inertia) CheckSSR(ctx context.Context) SSRHealth {
	health := SSRHealth{
		Enabled:  i.cfg.SSREnabled,
		URL:      i.cfg.SSRURL,
		Failures: SSRFailures(),
	}
	if !health.Enabled {
		return health
	}
	if health.URL == "" {
		health.Err = "server-side rendering is enabled but no render URL could be resolved"
		return health
	}

	client := &http.Client{Timeout: defaultSSRTimeout}
	if configured, ok := i.cfg.SSRHTTPClient.(*http.Client); ok && configured != nil {
		client = configured
	}

	probe := map[string]any{
		"component":      "__inertia_probe__",
		"props":          map[string]any{},
		"url":            "/",
		"version":        "",
		"encryptHistory": false,
		"clearHistory":   false,
	}
	body, err := json.Marshal(probe)
	if err != nil {
		health.Err = err.Error()
		return health
	}

	endpoint := strings.TrimSuffix(health.URL, "/render") + "/render"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		health.Err = err.Error()
		return health
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		health.Err = err.Error()
		return health
	}
	defer response.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		health.Err = err.Error()
		return health
	}
	if response.StatusCode != http.StatusOK {
		health.Err = fmt.Sprintf("render endpoint answered %d", response.StatusCode)
		return health
	}
	// A component the bundle does not know is expected to fail, but the
	// server must still answer with a body. An empty one means the bundle
	// never loaded.
	if len(bytes.TrimSpace(answer)) == 0 {
		health.Err = "render endpoint answered with an empty body, so the SSR bundle is not loaded"
		return health
	}

	health.OK = true
	return health
}

// ErrHotFileInProduction is returned when a production process finds a Vite
// hot file.
//
// It is the worst silent failure in the stack. detectSSRURL reads the hot
// file and points the renderer at a dev server that is not running, and
// viteTags points every asset at it too — so a production deploy serves a
// page whose CSS and JS 404 and whose markup is never server-rendered, with
// nothing in the logs about why. A stale ./public/hot committed or copied
// into an image is an easy mistake and a hard one to diagnose, so it is
// refused at boot instead.
var ErrHotFileInProduction = fmt.Errorf(
	"inertia: %s exists in production; it points assets and server-side rendering at a Vite dev server. "+
		"Delete it — `lemmego build` does, and it belongs in .gitignore", ViteHotPath)

// guardHotFile refuses to boot a production process with a hot file present.
func guardHotFile(inProduction bool) error {
	if !inProduction {
		return nil
	}
	if info, err := os.Stat(ViteHotPath); err == nil && !info.IsDir() {
		return ErrHotFileInProduction
	}
	return nil
}
