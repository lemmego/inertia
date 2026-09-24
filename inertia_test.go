package inertia

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lemmego/api/app"
)

func TestNewInertiaWithErrorMissingRoot(t *testing.T) {
	_, err := NewInertiaWithError(nil, filepath.Join(t.TempDir(), "missing.html"))
	if err == nil {
		t.Fatal("NewInertiaWithError() error = nil, want missing root error")
	}
	if !strings.Contains(err.Error(), "root template") {
		t.Fatalf("NewInertiaWithError() error = %q, want root template context", err)
	}
}

func TestNewInertiaCompatibilityWrapperReturnsNilOnError(t *testing.T) {
	got := NewInertia(nil, filepath.Join(t.TempDir(), "missing.html"))
	if got != nil {
		t.Fatal("NewInertia() returned a value for a missing root template")
	}
}

func TestProviderReturnsInitializationError(t *testing.T) {
	p := &Provider{RootTemplate: filepath.Join(t.TempDir(), "missing.html")}
	if err := p.Provide(app.Configure()); err == nil {
		t.Fatal("Provider.Provide() error = nil, want initialization error")
	}
}

func TestSetupViteMissingConfig(t *testing.T) {
	if err := setupVite(nil, nil); err == nil {
		t.Fatal("setupVite() error = nil, want missing config error")
	}
}

func TestWithViteOptions(t *testing.T) {
	cfg := Config{}
	WithVite(
		WithBuildManifest("manifest.json"),
		WithWaterfallPreload(7),
	).Apply(&cfg)

	if !cfg.ViteEnabled || cfg.ViteCfg == nil {
		t.Fatal("WithVite() did not enable Vite")
	}
	if cfg.ViteCfg.BuildManifest != "manifest.json" {
		t.Fatalf("BuildManifest = %q, want manifest.json", cfg.ViteCfg.BuildManifest)
	}
	if cfg.ViteCfg.PreloadStrategy != PreloadWaterfall || cfg.ViteCfg.PreloadConcurrent != 7 {
		t.Fatalf("preload configuration = %#v, want waterfall/7", cfg.ViteCfg)
	}
}

func TestViteMissingManifestReturnsErrorFromTemplateFunction(t *testing.T) {
	resolve := Vite(filepath.Join(t.TempDir(), "missing-manifest.json"), "/build/")
	_, err := resolve("app.js")
	if err == nil {
		t.Fatal("Vite() template function error = nil, want missing manifest error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Vite() error = %v, want os.ErrNotExist", err)
	}
}
