package inertia

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In development the dev server compiles CSS into a JavaScript module and
// answers a .css request with Content-Type: text/javascript. A <link> to that
// is inert, so the page paints unstyled until the application bundle runs.
// Asking for the same entry with ?direct returns real CSS, and a link to that
// blocks rendering as a stylesheet should.
func TestDevStylesheetIsRequestedAsRealCSS(t *testing.T) {
	dir := t.TempDir()
	hot := filepath.Join(dir, "hot")
	if err := os.WriteFile(hot, []byte("http://localhost:5173"), 0o600); err != nil {
		t.Fatal(err)
	}

	html, err := viteTags(hot, filepath.Join(dir, "manifest.json"), "build")("resources/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)

	if !strings.Contains(got, `<link rel="stylesheet" href="//localhost:5173/resources/css/app.css?direct">`) {
		t.Errorf("no render-blocking stylesheet for the dev server:\n%s", got)
	}
	// The module form is what the dev server hot-updates, so it is emitted too.
	if !strings.Contains(got, `<script type="module" src="//localhost:5173/resources/css/app.css"></script>`) {
		t.Errorf("no module tag, so CSS would not hot-update:\n%s", got)
	}
}

func TestDevScriptEntryIsAModule(t *testing.T) {
	dir := t.TempDir()
	hot := filepath.Join(dir, "hot")
	if err := os.WriteFile(hot, []byte("http://localhost:5173"), 0o600); err != nil {
		t.Fatal(err)
	}

	html, _ := viteTags(hot, "", "build")("resources/js/app.tsx")
	got := string(html)

	if !strings.Contains(got, `<script type="module" src="//localhost:5173/resources/js/app.tsx"></script>`) {
		t.Errorf("unexpected dev script tag:\n%s", got)
	}
	// ?direct is for stylesheets only.
	if strings.Contains(got, "?direct") {
		t.Errorf("a script entry was requested with ?direct:\n%s", got)
	}
}

// A build produces real files, so a plain stylesheet link is correct and
// ?direct would be wrong.
func TestBuildStylesheetIsAPlainLink(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{
		"resources/css/app.css": {"file": "assets/app-abc.css", "isEntry": true}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	html, err := viteTags(filepath.Join(dir, "absent-hot"), manifest, "build")("resources/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)

	if !strings.Contains(got, `<link rel="stylesheet" href="/build/assets/app-abc.css">`) {
		t.Errorf("unexpected built stylesheet tag:\n%s", got)
	}
	if strings.Contains(got, "?direct") {
		t.Errorf("a built stylesheet was requested with ?direct:\n%s", got)
	}
	if strings.Contains(got, "localhost") {
		t.Errorf("a built asset pointed at the dev server:\n%s", got)
	}
}

// A built script names the stylesheets it imported. Emitting those as links
// means the browser has them before first paint, rather than after the bundle
// has downloaded and run.
func TestBuildScriptEmitsItsStylesheets(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{
		"resources/js/app.tsx": {"file": "assets/app-xyz.js", "isEntry": true, "css": ["assets/app-abc.css"]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	html, _ := viteTags(filepath.Join(dir, "absent-hot"), manifest, "build")("resources/js/app.tsx")
	got := string(html)

	css := strings.Index(got, `<link rel="stylesheet"`)
	js := strings.Index(got, `<script type="module"`)
	if css < 0 {
		t.Fatalf("the script's stylesheet was not emitted:\n%s", got)
	}
	if js < 0 {
		t.Fatalf("the script was not emitted:\n%s", got)
	}
	if css > js {
		t.Error("the stylesheet is emitted after the script; it should be fetched first")
	}
}

// Asking for both a CSS entry and the script that imports it must not produce
// the same stylesheet twice.
func TestStylesheetsAreNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{
		"resources/css/app.css": {"file": "assets/app-abc.css", "isEntry": true},
		"resources/js/app.tsx": {"file": "assets/app-xyz.js", "isEntry": true, "css": ["assets/app-abc.css"]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	html, _ := viteTags(filepath.Join(dir, "absent-hot"), manifest, "build")(
		"resources/css/app.css", "resources/js/app.tsx")
	got := string(html)

	if n := strings.Count(got, "assets/app-abc.css"); n != 1 {
		t.Errorf("the stylesheet appears %d times, want 1:\n%s", n, got)
	}
}

func TestUnknownEntryIsSkippedRatherThanBreakingThePage(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	html, err := viteTags(filepath.Join(dir, "absent-hot"), manifest, "build")("resources/js/missing.tsx")
	if err != nil {
		t.Fatalf("an unknown entry returned an error: %v", err)
	}
	if strings.TrimSpace(string(html)) != "" {
		t.Errorf("unexpected output for an unknown entry: %q", html)
	}
}
