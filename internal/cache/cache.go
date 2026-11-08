// Package cache keeps an in-memory view of every application on every ArgoCD instance,
// fed by one list+watch loop per instance.
package cache

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/snapp-incubator/argo-center/internal/argocd"
)

// Source is one ArgoCD instance to watch.
type Source struct {
	Key         string
	DisplayName string
	URL         string
	Client      *argocd.Client
}

const (
	StateConnecting = "connecting"
	StateOK         = "ok"
	StateError      = "error"
)

type ClusterState struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
	// State is connecting, ok or error. On error, apps keep their last known state.
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	LastUpdate time.Time `json:"lastUpdate,omitzero"`
	AppCount   int       `json:"appCount"`
}

// AppRow is one app across clusters. Cells has an entry only for clusters where the app exists.
type AppRow struct {
	Name  string                `json:"name"`
	Cells map[string]AppSummary `json:"cells"`
}

type Event struct {
	// Type is app-changed, app-deleted, cluster-changed or rollout-updated.
	Type    string `json:"type"`
	Cluster string `json:"cluster"`
	App     string `json:"app,omitempty"`
}

type Cache struct {
	sources []Source
	resync  time.Duration

	mu       sync.RWMutex
	apps     map[string]map[string]AppSummary // cluster key -> app name -> summary
	clusters map[string]*ClusterState

	bus bus
}

func New(sources []Source, resync time.Duration) *Cache {
	c := &Cache{
		sources:  sources,
		resync:   resync,
		apps:     map[string]map[string]AppSummary{},
		clusters: map[string]*ClusterState{},
		bus:      bus{subs: map[chan Event]struct{}{}},
	}
	for _, s := range sources {
		c.apps[s.Key] = map[string]AppSummary{}
		c.clusters[s.Key] = &ClusterState{Key: s.Key, DisplayName: s.DisplayName, URL: s.URL, State: StateConnecting}
	}
	return c
}

// Run watches all sources until ctx is cancelled.
func (c *Cache) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range c.sources {
		wg.Go(func() { c.runSource(ctx, s) })
	}
	wg.Wait()
}

func (c *Cache) runSource(ctx context.Context, src Source) {
	log := slog.With("cluster", src.Key)
	backoff := time.Second
	for {
		err := c.listAndWatch(ctx, src)
		if ctx.Err() != nil {
			return
		}
		wait := time.Duration(0)
		if err == nil {
			backoff = time.Second
		} else {
			log.Warn("argocd connection failed", "err", err, "retryIn", backoff.String())
			c.setState(src.Key, StateError, err.Error())
			wait = backoff
			backoff = min(backoff*2, 30*time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// minStreamLife is how long a watch stream must stay up for its interruption to count as routine.
const minStreamLife = 5 * time.Second

// listAndWatch does a full list, then watches until the resync interval elapses (returning nil) or the
// watch fails.
//
// The proxy in front of ArgoCD cuts long responses (Envoy: 504 or a stream reset after 30s), so an
// interrupted stream is resumed from the last seen resourceVersion, without a new list and without
// marking the cluster as failing. Events in the short gap are not replayed by ArgoCD; the periodic
// re-list catches them. Streams that keep dying within seconds count as a failure.
func (c *Cache) listAndWatch(ctx context.Context, src Source) error {
	list, err := src.Client.ListApplications(ctx)
	if err != nil {
		return err
	}
	fresh := make(map[string]AppSummary, len(list.Items))
	for _, a := range list.Items {
		s := Summarize(a, src.URL)
		fresh[s.Name] = s
	}
	c.replace(src.Key, fresh)

	wctx, cancel := context.WithTimeout(ctx, c.resync)
	defer cancel()
	rv := list.Metadata.ResourceVersion
	quickCuts := 0
	for {
		started := time.Now()
		err := src.Client.WatchApplications(wctx, rv, func(ev argocd.WatchEvent) error {
			rv = newerVersion(rv, ev.Application.Metadata.ResourceVersion)
			c.apply(src, ev)
			return nil
		})
		if wctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil // resync interval elapsed
		}
		if !interrupted(err) {
			return err
		}
		if time.Since(started) >= minStreamLife {
			quickCuts = 0
		} else if quickCuts++; quickCuts >= 3 {
			return fmt.Errorf("watch stream keeps breaking: %w", err)
		}
	}
}

// interrupted reports whether a watch ended the routine way: cut by the proxy or closed by ArgoCD.
func interrupted(err error) bool {
	var apiErr *argocd.APIError
	return errors.Is(err, argocd.ErrStreamInterrupted) ||
		errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusGatewayTimeout
}

// newerVersion returns the larger of two Kubernetes resource versions (decimal strings in practice).
func newerVersion(cur, v string) string {
	a, errA := strconv.ParseUint(cur, 10, 64)
	b, errB := strconv.ParseUint(v, 10, 64)
	switch {
	case errB != nil:
		return cur
	case errA != nil || b > a:
		return v
	}
	return cur
}

func (c *Cache) replace(cluster string, fresh map[string]AppSummary) {
	c.mu.Lock()
	old := c.apps[cluster]
	c.apps[cluster] = fresh
	st := c.clusters[cluster]
	st.State, st.Error, st.LastUpdate, st.AppCount = StateOK, "", time.Now().UTC(), len(fresh)
	c.mu.Unlock()

	for name, s := range fresh {
		if prev, ok := old[name]; !ok || !sameIgnoringReconcile(prev, s) {
			c.bus.publish(Event{Type: "app-changed", Cluster: cluster, App: name})
		}
	}
	for name := range old {
		if _, ok := fresh[name]; !ok {
			c.bus.publish(Event{Type: "app-deleted", Cluster: cluster, App: name})
		}
	}
	c.bus.publish(Event{Type: "cluster-changed", Cluster: cluster})
}

func (c *Cache) apply(src Source, ev argocd.WatchEvent) {
	name := ev.Application.Metadata.Name
	if name == "" {
		return
	}
	c.mu.Lock()
	apps := c.apps[src.Key]
	st := c.clusters[src.Key]
	st.LastUpdate = time.Now().UTC()
	var event string
	if ev.Type == "DELETED" {
		if _, ok := apps[name]; ok {
			delete(apps, name)
			event = "app-deleted"
		}
	} else {
		s := Summarize(ev.Application, src.URL)
		if prev, ok := apps[name]; !ok || !sameIgnoringReconcile(prev, s) {
			event = "app-changed"
		}
		apps[name] = s
	}
	st.AppCount = len(apps)
	c.mu.Unlock()

	if event != "" {
		c.bus.publish(Event{Type: event, Cluster: src.Key, App: name})
	}
}

func (c *Cache) setState(cluster, state, errMsg string) {
	c.mu.Lock()
	st := c.clusters[cluster]
	changed := st.State != state || st.Error != errMsg
	st.State, st.Error = state, errMsg
	c.mu.Unlock()
	if changed {
		c.bus.publish(Event{Type: "cluster-changed", Cluster: cluster})
	}
}

// Clusters returns cluster states in config order.
func (c *Cache) Clusters() []ClusterState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ClusterState, 0, len(c.sources))
	for _, s := range c.sources {
		out = append(out, *c.clusters[s.Key])
	}
	return out
}

// Apps returns every app found on any cluster, sorted by name.
func (c *Cache) Apps() []AppRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rows := map[string]*AppRow{}
	for cluster, apps := range c.apps {
		for name, s := range apps {
			r, ok := rows[name]
			if !ok {
				r = &AppRow{Name: name, Cells: map[string]AppSummary{}}
				rows[name] = r
			}
			r.Cells[cluster] = s
		}
	}
	out := make([]AppRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b AppRow) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// App returns one app across clusters, or false if no cluster has it.
func (c *Cache) App(name string) (AppRow, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	row := AppRow{Name: name, Cells: map[string]AppSummary{}}
	for cluster, apps := range c.apps {
		if s, ok := apps[name]; ok {
			row.Cells[cluster] = s
		}
	}
	return row, len(row.Cells) > 0
}

// Put stores an application fetched outside the watch, such as the result of a refresh, so readers
// see it without waiting for the watch to deliver the same change.
func (c *Cache) Put(cluster string, app argocd.Application) {
	for _, s := range c.sources {
		if s.Key == cluster {
			c.apply(s, argocd.WatchEvent{Type: "MODIFIED", Application: app})
		}
	}
}

// Lookup returns the source for a cluster and the app's summary there.
// The summary is valid only when found is true; knownCluster is false for an unknown cluster key.
func (c *Cache) Lookup(cluster, app string) (src Source, summary AppSummary, knownCluster, found bool) {
	for _, s := range c.sources {
		if s.Key == cluster {
			src, knownCluster = s, true
		}
	}
	if !knownCluster {
		return
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	summary, found = c.apps[cluster][app]
	return
}

// Publish sends an event to all subscribers, e.g. rollout-updated from the rollout engine.
func (c *Cache) Publish(e Event) {
	c.bus.publish(e)
}

// Subscribe returns a channel of change events and a function to stop receiving them.
// Slow subscribers miss events rather than block the cache.
func (c *Cache) Subscribe() (<-chan Event, func()) {
	return c.bus.subscribe()
}

type bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func (b *bus) subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *bus) publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
