// Package diff turns ArgoCD's managed-resources response into a brief and a full diff per resource:
// the list of changed field paths, and a unified YAML diff of live state against what a sync would apply.
package diff

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"gopkg.in/yaml.v3"

	"github.com/snapp-incubator/argo-center/internal/argocd"
)

const (
	// StatusModified: exists in Git and in the cluster, and differs.
	StatusModified = "modified"
	// StatusMissing: in Git but not in the cluster; a sync creates it.
	StatusMissing = "missing"
	// StatusExtra: in the cluster but no longer in Git; a sync deletes it only with prune.
	StatusExtra = "extra"
)

const (
	OpChanged = "changed"
	OpAdded   = "added"   // present after sync, absent now
	OpRemoved = "removed" // present now, absent after sync
)

// Limits keep one huge resource (a CRD with a rewritten schema, say) from bloating the response.
const (
	maxChanges     = 25
	maxUnifiedDiff = 256 << 10
)

type Change struct {
	Path string `json:"path"`
	Op   string `json:"op"`
}

type Resource struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// Changes lists changed field paths for modified resources, at most maxChanges of them;
	// MoreChanges counts the rest.
	Changes     []Change `json:"changes"`
	MoreChanges int      `json:"moreChanges,omitempty"`
	// UnifiedDiff is a diff of live YAML ("-") against the YAML after sync ("+"), without file headers.
	UnifiedDiff string `json:"unifiedDiff"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// Compute returns the resources that a sync would change, sorted by kind, namespace and name.
// It compares normalizedLiveState with predictedLiveState, as the ArgoCD UI does, after removing
// server-managed noise such as managedFields and status.
func Compute(items []argocd.ManagedResource) ([]Resource, error) {
	out := []Resource{}
	for _, it := range items {
		if it.Hook {
			continue
		}
		live, err := parse(it.NormalizedLiveState)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: live state: %w", it.Kind, it.Name, err)
		}
		target, err := parse(it.PredictedLiveState)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: predicted state: %w", it.Kind, it.Name, err)
		}
		if target == nil && !isNull(it.TargetState) {
			// predictedLiveState is absent when ArgoCD could not compute it; fall back to Git.
			if target, err = parse(it.TargetState); err != nil {
				return nil, fmt.Errorf("%s/%s: target state: %w", it.Kind, it.Name, err)
			}
		}

		r := Resource{Group: it.Group, Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Changes: []Change{}}
		switch {
		case live == nil && target != nil:
			r.Status = StatusMissing
		case live != nil && isNull(it.TargetState):
			r.Status = StatusExtra
		case it.Modified && live != nil && target != nil:
			r.Status = StatusModified
			changes := Paths(live, target)
			if len(changes) > maxChanges {
				r.MoreChanges = len(changes) - maxChanges
				changes = changes[:maxChanges]
			}
			r.Changes = changes
		default:
			continue
		}
		if r.UnifiedDiff, r.Truncated, err = unified(live, target); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", it.Kind, it.Name, err)
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Resource) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func isNull(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "null"
}

// parse decodes a JSON object and strips noise. It returns nil for "null" or an empty string.
func parse(s string) (map[string]any, error) {
	if isNull(s) {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	obj, ok := numbers(v).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	clean(obj)
	return obj, nil
}

// numbers replaces json.Number with int64 or float64, so YAML shows 8080 rather than "8080".
func numbers(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			v[k] = numbers(e)
		}
	case []any:
		for i, e := range v {
			v[i] = numbers(e)
		}
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	}
	return v
}

// clean removes fields the server owns, which never matter for a review.
func clean(obj map[string]any) {
	delete(obj, "status")
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return
	}
	for _, k := range []string{"managedFields", "resourceVersion", "uid", "generation", "creationTimestamp", "selfLink"} {
		delete(meta, k)
	}
	if ann, _ := meta["annotations"].(map[string]any); ann != nil {
		delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
		if len(ann) == 0 {
			delete(meta, "annotations")
		}
	}
}

// Paths returns the field paths that differ between a (live) and b (after sync), in sorted order.
// A subtree present on only one side is reported once, at its root.
// Lists whose items all have a unique "name" are matched by name: containers[app].image.
// The result is never nil, so it is "[]" rather than "null" in JSON.
func Paths(a, b any) []Change {
	out := []Change{}
	walk("", a, b, &out)
	return out
}

func walk(path string, a, b any, out *[]Change) {
	switch {
	case a == nil && b == nil:
		return
	case a == nil:
		*out = append(*out, Change{Path: path, Op: OpAdded})
		return
	case b == nil:
		*out = append(*out, Change{Path: path, Op: OpRemoved})
		return
	}

	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := make([]string, 0, len(av)+len(bv))
		for k := range av {
			keys = append(keys, k)
		}
		for k := range bv {
			if _, ok := av[k]; !ok {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		for _, k := range keys {
			walk(joinKey(path, k), av[k], bv[k], out)
		}
		return
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		walkList(path, av, bv, out)
		return
	default:
		if a == b {
			return
		}
	}
	*out = append(*out, Change{Path: path, Op: OpChanged})
}

func walkList(path string, a, b []any, out *[]Change) {
	an, aok := byName(a)
	bn, bok := byName(b)
	if aok && bok {
		names := make([]string, 0, len(an)+len(bn))
		seen := map[string]bool{}
		for _, list := range [][]any{a, b} {
			for _, e := range list {
				n := e.(map[string]any)["name"].(string)
				if !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			walk(path+"["+n+"]", an[n], bn[n], out)
		}
		return
	}
	for i := range max(len(a), len(b)) {
		var ae, be any
		if i < len(a) {
			ae = a[i]
		}
		if i < len(b) {
			be = b[i]
		}
		walk(path+"["+strconv.Itoa(i)+"]", ae, be, out)
	}
}

// byName indexes list items by their "name" field, if every item is an object with a unique name.
func byName(list []any) (map[string]any, bool) {
	if len(list) == 0 {
		return map[string]any{}, true
	}
	m := make(map[string]any, len(list))
	for _, e := range list {
		obj, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		n, ok := obj["name"].(string)
		if !ok || m[n] != nil {
			return nil, false
		}
		m[n] = obj
	}
	return m, true
}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func joinKey(path, key string) string {
	if !plainKey.MatchString(key) {
		return path + "[" + strconv.Quote(key) + "]"
	}
	if path == "" {
		return key
	}
	return path + "." + key
}

func toYAML(obj map[string]any) (string, error) {
	if obj == nil {
		return "", nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(obj); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// tidy returns a copy of v for display. YAML prints a multiline string as a block scalar (|) only
// when no line ends in a space or tab and lines end in "\n"; otherwise it prints one long quoted
// line, which makes a changed ConfigMap value unreadable. tidy drops trailing spaces and CRLFs from
// such strings. other is the value at the same place on the other side: when tidying would hide
// the only change to a string, the string is kept as is.
func tidy(v, other any) any {
	switch v := v.(type) {
	case map[string]any:
		o, _ := other.(map[string]any)
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = tidy(e, o[k])
		}
		return out
	case []any:
		o, _ := other.([]any)
		out := make([]any, len(v))
		for i, e := range v {
			var oe any
			if i < len(o) {
				oe = o[i]
			}
			out[i] = tidy(e, oe)
		}
		return out
	case string:
		t := tidyString(v)
		if o, ok := other.(string); ok && o != v && tidyString(o) == t {
			return v
		}
		return t
	}
	return v
}

var trailingBlanks = regexp.MustCompile(`[ \t]+(\n|$)`)

func tidyString(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return trailingBlanks.ReplaceAllString(s, "$1")
}

func unified(live, target map[string]any) (string, bool, error) {
	if live != nil && target != nil {
		live, target = tidy(live, target).(map[string]any), tidy(target, live).(map[string]any)
	} else if live != nil {
		live = tidy(live, nil).(map[string]any)
	} else if target != nil {
		target = tidy(target, nil).(map[string]any)
	}
	// (Go evaluates both tidy calls above before assigning, so each sees the other's raw side.)
	before, err := toYAML(live)
	if err != nil {
		return "", false, err
	}
	after, err := toYAML(target)
	if err != nil {
		return "", false, err
	}
	d, err := udiff.ToUnified("live", "target", before, udiff.Lines(before, after), udiff.DefaultContextLines)
	if err != nil {
		return "", false, err
	}
	// Drop the "--- live" / "+++ target" header; the UI labels the sides itself.
	if _, rest, ok := strings.Cut(d, "\n+++ target\n"); ok {
		d = rest
	}
	if len(d) > maxUnifiedDiff {
		cut := strings.LastIndexByte(d[:maxUnifiedDiff], '\n')
		return d[:cut+1], true, nil
	}
	return d, false, nil
}
