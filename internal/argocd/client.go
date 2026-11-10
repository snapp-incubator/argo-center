// Package argocd is a thin client for the parts of the ArgoCD REST API argo-center uses.
package argocd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Options struct {
	BaseURL            string
	Token              string
	CAFile             string
	InsecureSkipVerify bool
}

type Client struct {
	baseURL string
	token   string
	// api is used for request/response calls; stream has no overall timeout and relies on the context.
	api    *http.Client
	stream *http.Client
}

func New(opts Options) (*Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: opts.InsecureSkipVerify} //nolint:gosec // opt-in per cluster
	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s: no certificates found", opts.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsCfg

	return &Client{
		baseURL: strings.TrimRight(opts.BaseURL, "/"),
		token:   opts.Token,
		api:     &http.Client{Transport: transport, Timeout: 30 * time.Second},
		stream:  &http.Client{Transport: transport},
	}, nil
}

// APIError is a non-2xx response from ArgoCD.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("token rejected by ArgoCD (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("ArgoCD returned HTTP %d: %s", e.StatusCode, e.Message)
}

// ListApplications returns all applications the token can see.
//
// Full objects are fetched on purpose: ArgoCD's ?fields= filter only honors the exact paths its own
// UI requests and silently drops others (status.sync.revision, status.history, ...).
func (c *Client) ListApplications(ctx context.Context) (*ApplicationList, error) {
	resp, err := c.do(ctx, c.api, "/api/v1/applications", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var list ApplicationList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode application list: %w", err)
	}
	return &list, nil
}

// WatchApplications streams application changes newer than resourceVersion, calling fn for each.
// It blocks until the stream ends, ctx is cancelled, or fn returns an error.
// A stream that ends or breaks after it was established returns an error wrapping ErrStreamInterrupted.
func (c *Client) WatchApplications(ctx context.Context, resourceVersion string, fn func(WatchEvent) error) error {
	q := url.Values{}
	if resourceVersion != "" {
		q.Set("resourceVersion", resourceVersion)
	}

	resp, err := c.do(ctx, c.stream, "/api/v1/stream/applications", q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Result *WatchEvent `json:"result"`
			Error  *struct {
				HTTPCode int    `json:"http_code"`
				Message  string `json:"message"`
			} `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return ErrStreamInterrupted
			}
			return fmt.Errorf("%w: %v", ErrStreamInterrupted, err)
		}
		if msg.Error != nil {
			return &APIError{StatusCode: msg.Error.HTTPCode, Message: msg.Error.Message}
		}
		if msg.Result == nil {
			continue
		}
		if err := fn(*msg.Result); err != nil {
			return err
		}
	}
}

// ErrStreamInterrupted means a watch stream ended after it was established. Proxies in front of
// ArgoCD do this routinely: an Envoy-based ingress, for example, cuts any response after its timeout (often 30s).
var ErrStreamInterrupted = errors.New("watch stream interrupted")

// ManagedResources returns the target and live state of every resource the application manages.
func (c *Client) ManagedResources(ctx context.Context, app, appNamespace string) ([]ManagedResource, error) {
	resp, err := c.do(ctx, c.api, "/api/v1/applications/"+url.PathEscape(app)+"/managed-resources", nsQuery(appNamespace))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Items []ManagedResource `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode managed resources: %w", err)
	}
	return body.Items, nil
}

// RefreshApplication asks ArgoCD to compare the application with Git again and returns it once the
// comparison is done. A hard refresh also discards ArgoCD's cached manifests. ArgoCD holds the request
// open until the refresh finishes, which can take minutes for a hard refresh, so the caller sets the
// deadline through ctx.
func (c *Client) RefreshApplication(ctx context.Context, app, appNamespace string, hard bool) (*Application, error) {
	q := nsQuery(appNamespace)
	q.Set("refresh", "normal")
	if hard {
		q.Set("refresh", "hard")
	}
	resp, err := c.do(ctx, c.stream, "/api/v1/applications/"+url.PathEscape(app), q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var a Application
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("decode application: %w", err)
	}
	return &a, nil
}

// GetApplication returns the application as ArgoCD currently sees it.
func (c *Client) GetApplication(ctx context.Context, app, appNamespace string) (*Application, error) {
	resp, err := c.do(ctx, c.api, "/api/v1/applications/"+url.PathEscape(app), nsQuery(appNamespace))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var a Application
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("decode application: %w", err)
	}
	return &a, nil
}

// SetTargetRevision changes the revision the application tracks (spec.source.targetRevision) to a
// branch, tag or commit, and returns the updated application. ArgoCD compares the app with the new
// revision by itself afterwards. The token needs the `applications, update` right.
func (c *Client) SetTargetRevision(ctx context.Context, app, appNamespace, revision string) (*Application, error) {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"source": map[string]any{"targetRevision": revision}}})
	if err != nil {
		return nil, err
	}
	req := PatchRequest{Name: app, AppNamespace: appNamespace, Patch: string(patch), PatchType: "merge"}
	resp, err := c.send(ctx, c.api, http.MethodPatch, "/api/v1/applications/"+url.PathEscape(app), nil, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var a Application
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("decode application: %w", err)
	}
	return &a, nil
}

// Sync starts a sync operation. It returns once ArgoCD has accepted the operation, not when it finishes;
// progress shows up in the application's operationState.
func (c *Client) Sync(ctx context.Context, app string, req SyncRequest) error {
	resp, err := c.send(ctx, c.api, http.MethodPost, "/api/v1/applications/"+url.PathEscape(app)+"/sync", nil, req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// TerminateOperation stops the application's running operation.
func (c *Client) TerminateOperation(ctx context.Context, app, appNamespace string) error {
	resp, err := c.send(ctx, c.api, http.MethodDelete, "/api/v1/applications/"+url.PathEscape(app)+"/operation", nsQuery(appNamespace), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func nsQuery(appNamespace string) url.Values {
	q := url.Values{}
	if appNamespace != "" {
		q.Set("appNamespace", appNamespace)
	}
	return q
}

func (c *Client) do(ctx context.Context, hc *http.Client, path string, q url.Values) (*http.Response, error) {
	return c.send(ctx, hc, http.MethodGet, path, q, nil)
}

func (c *Client) send(ctx context.Context, hc *http.Client, method, path string, q url.Values, body any) (*http.Response, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := hc.Do(req)
	if err != nil {
		// Drop the URL from the error: it is shown in the UI and the path alone is enough.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, &APIError{StatusCode: resp.StatusCode, Message: errorMessage(resp.Body)}
	}
	return resp, nil
}

// errorMessage extracts the message from an ArgoCD (grpc-gateway) error body, falling back to raw text.
func errorMessage(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error != "" {
			return e.Error
		}
	}
	return strings.TrimSpace(string(raw))
}
