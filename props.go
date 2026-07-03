package inertia

import (
	"github.com/romsar/gonertia/v3"
)

// ---------------------------------------------------------------------------
// Prop types — lemmego-owned wrappers that translate to gonertia types
// ---------------------------------------------------------------------------

// OptionalProp is a property evaluated only when requested in a partial reload.
// https://inertiajs.com/partial-reloads
type OptionalProp struct {
	Value any
}

func (p OptionalProp) Prop() any { return p.Value }

// Optional creates an optional (lazy) prop value.
func Optional(value any) OptionalProp { return OptionalProp{Value: value} }

// AlwaysProp is a property always included, even during partial reloads.
type AlwaysProp struct {
	Value any
}

func (p AlwaysProp) Prop() any { return p.Value }

// Always creates an always-evaluated prop value.
func Always(value any) AlwaysProp { return AlwaysProp{Value: value} }

// DeferProp is a property evaluated after the page has loaded.
// https://inertiajs.com/deferred-props
type DeferProp struct {
	Value any
	Group string
	merge bool
}

func (p DeferProp) Prop() any { return p.Value }

// Merge makes the deferred prop merge instead of overwrite on subsequent loads.
func (p DeferProp) Merge() DeferProp {
	p.merge = true
	return p
}

// Defer creates a deferred prop value that loads after the initial page render.
func Defer(value any, group ...string) DeferProp {
	g := "default"
	if len(group) > 0 {
		g = group[0]
	}
	return DeferProp{Value: value, Group: g}
}

// OnceProp is a property sent only on the first visit, not on partial reloads
// unless explicitly requested.
// https://inertiajs.com/docs/v2/data-props/once-props
type OnceProp struct {
	Value any
}

func (p OnceProp) Prop() any { return p.Value }

// Once creates a once-only prop value.
func Once(value any) OnceProp { return OnceProp{Value: value} }

// MergeProps is a property whose value is merged with previous values
// instead of being overwritten.
// https://inertiajs.com/merging-props
type MergeProps struct {
	Value          any
	deepMerge      bool
	matchOn        []string
	appendAtPaths  []string
	prependAtPaths []string
}

func (p MergeProps) Prop() any { return p.Value }

// Merge sets the prop to merge (append) on subsequent updates.
func (p MergeProps) Merge() MergeProps {
	p.deepMerge = false
	return p
}

// DeepMerge sets the prop to deep-merge on subsequent updates.
func (p MergeProps) DeepMerge() MergeProps {
	p.deepMerge = true
	return p
}

// MatchOn restricts the merge behavior to only re-run when the listed keys change.
func (p MergeProps) MatchOn(keys ...string) MergeProps {
	p.matchOn = append(p.matchOn, keys...)
	return p
}

// Append marks the prop as appended (additive merge).
func (p MergeProps) Append(paths ...string) MergeProps {
	p.appendAtPaths = append(p.appendAtPaths, paths...)
	return p
}

// Prepend marks the prop as prepended.
func (p MergeProps) Prepend(paths ...string) MergeProps {
	p.prependAtPaths = append(p.prependAtPaths, paths...)
	return p
}

// Merge creates a mergeable prop value.
func Merge(value any) MergeProps {
	return MergeProps{Value: value}
}

// DeepMerge creates a deep-mergeable prop value.
func DeepMerge(value any) MergeProps {
	return MergeProps{Value: value, deepMerge: true}
}

// ScrollMetadata holds pagination metadata for infinite scroll.
type ScrollMetadata struct {
	PageName     string
	PreviousPage any
	NextPage     any
	CurrentPage  any
}

// ScrollProp is a property for infinite scrolling / pagination.
// https://inertiajs.com/infinite-scroll
type ScrollProp struct {
	Value    any
	Wrapper  string
	Metadata ScrollMetadata
}

func (p ScrollProp) Prop() any { return p.Value }

// ScrollOption configures a ScrollProp.
type ScrollOption func(*ScrollProp)

// WithWrapper sets a custom wrapper key for the scroll prop (defaults to "data").
func WithWrapper(wrapper string) ScrollOption {
	return func(p *ScrollProp) { p.Wrapper = wrapper }
}

// WithMetadata sets pagination metadata for the scroll prop.
func WithMetadata(metadata ScrollMetadata) ScrollOption {
	return func(p *ScrollProp) { p.Metadata = metadata }
}

// Scroll creates a scroll prop for infinite scrolling.
func Scroll(value any, opts ...ScrollOption) ScrollProp {
	p := ScrollProp{Value: value, Wrapper: "data"}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

// ---------------------------------------------------------------------------
// Conversion — lemmego prop types → gonertia prop types
// ---------------------------------------------------------------------------

// toGonertiaProps converts a map with lemmego prop types into a gonertia.Props
// map so that gonertia's resolvePropVal can recognise them.
func toGonertiaProps(props map[string]any) gonertia.Props {
	result := make(gonertia.Props, len(props))
	for k, v := range props {
		result[k] = toGonertiaProp(v)
	}
	return result
}

func toGonertiaProp(v any) any {
	switch val := v.(type) {
	case OptionalProp:
		return gonertia.Optional(val.Value)
	case AlwaysProp:
		return gonertia.Always(val.Value)
	case OnceProp:
		return gonertia.Once(val.Value)
	case DeferProp:
		g := val.Group
		if g == "" {
			g = "default"
		}
		d := gonertia.Defer(val.Value, g)
		if val.merge {
			d = d.Merge()
		}
		return d
	case MergeProps:
		var m gonertia.MergeProps
		if val.deepMerge {
			m = gonertia.DeepMerge(val.Value)
		} else {
			m = gonertia.Merge(val.Value)
		}
		if len(val.matchOn) > 0 {
			m = m.MatchOn(val.matchOn...)
		}
		if len(val.appendAtPaths) > 0 {
			m = m.Append(val.appendAtPaths...)
		}
		if len(val.prependAtPaths) > 0 {
			m = m.Prepend(val.prependAtPaths...)
		}
		return m
	case ScrollProp:
		opts := []gonertia.ScrollOption{}
		if val.Wrapper != "" {
			opts = append(opts, gonertia.WithWrapper(val.Wrapper))
		}
		if val.Metadata.PageName != "" || val.Metadata.CurrentPage != nil ||
			val.Metadata.PreviousPage != nil || val.Metadata.NextPage != nil {
			opts = append(opts, gonertia.WithMetadata(gonertia.ScrollMetadata{
				PageName:     val.Metadata.PageName,
				PreviousPage: val.Metadata.PreviousPage,
				NextPage:     val.Metadata.NextPage,
				CurrentPage:  val.Metadata.CurrentPage,
			}))
		}
		return gonertia.Scroll(val.Value, opts...)
	default:
		return v
	}
}
