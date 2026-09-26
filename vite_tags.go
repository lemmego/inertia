package inertia

import (
	"encoding/json"
	"html/template"
	"os"
	"path"
	"strings"
)

// viteTags renders the markup an entry needs, which is not the same in
// development as in a build.
//
// A built stylesheet is a plain file and a <link> loads it. The dev server does
// not serve one: it compiles CSS into a JavaScript module that injects the
// styles at runtime, so it answers a request for a .css entry with
// Content-Type: text/javascript. A browser refuses to apply that as a
// stylesheet, which leaves the page unstyled until the application bundle has
// downloaded and run — the flash of unstyled content, and the more visible the
// slower the connection.
//
// Asking the dev server for the same entry with ?direct gets the compiled CSS
// with the right content type, so the <link> blocks rendering as it should and
// the page never paints unstyled. The module form is emitted too, because that
// is the one the dev server hot-updates when the CSS changes. Both carry the
// same rules, so loading both changes nothing except that the page is styled
// from the first paint.
func viteTags(hotPath, manifestPath, buildDir string) func(entries ...string) (template.HTML, error) {
	return func(entries ...string) (template.HTML, error) {
		if devBase := readViteHotURL(hotPath); devBase != "" {
			return devTags(devBase, entries), nil
		}
		return buildTags(manifestPath, buildDir, entries)
	}
}

func devTags(base string, entries []string) template.HTML {
	var out strings.Builder
	for _, entry := range entries {
		url := base + "/" + strings.TrimPrefix(entry, "/")
		if isStylesheet(entry) {
			// Render-blocking, correctly typed, so the first paint is styled.
			out.WriteString(`<link rel="stylesheet" href="` + template.HTMLEscapeString(url+"?direct") + `">`)
			out.WriteString("\n    ")
			// And the module the dev server hot-updates.
			out.WriteString(`<script type="module" src="` + template.HTMLEscapeString(url) + `"></script>`)
		} else {
			out.WriteString(`<script type="module" src="` + template.HTMLEscapeString(url) + `"></script>`)
		}
		out.WriteString("\n    ")
	}
	return template.HTML(out.String())
}

// manifestEntry is the part of a Vite manifest record this needs.
type manifestEntry struct {
	File string   `json:"file"`
	CSS  []string `json:"css"`
}

func buildTags(manifestPath, buildDir string, entries []string) (template.HTML, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", err
	}
	var manifest map[string]manifestEntry
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", err
	}

	var out strings.Builder
	seen := map[string]bool{}

	emitStylesheet := func(file string) {
		href := path.Join("/", buildDir, file)
		if seen[href] {
			return
		}
		seen[href] = true
		out.WriteString(`<link rel="stylesheet" href="` + template.HTMLEscapeString(href) + `">`)
		out.WriteString("\n    ")
	}

	for _, entry := range entries {
		record, ok := manifest[entry]
		if !ok {
			continue
		}

		// A JavaScript entry names the stylesheets it pulled in. Emitting them
		// as links means the browser fetches them in parallel with the script
		// and has them before first paint, rather than after the bundle runs.
		for _, css := range record.CSS {
			emitStylesheet(css)
		}

		if isStylesheet(entry) {
			emitStylesheet(record.File)
			continue
		}
		src := path.Join("/", buildDir, record.File)
		out.WriteString(`<script type="module" src="` + template.HTMLEscapeString(src) + `"></script>`)
		out.WriteString("\n    ")
	}
	return template.HTML(out.String()), nil
}

func isStylesheet(entry string) bool {
	switch path.Ext(strings.SplitN(entry, "?", 2)[0]) {
	case ".css", ".scss", ".sass", ".less", ".styl", ".stylus", ".pcss", ".postcss":
		return true
	}
	return false
}
