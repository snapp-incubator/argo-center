// Package auth logs users in through OIDC and keeps them logged in with a signed session cookie.
package auth

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "argo_center_session"
	loginCookie   = "argo_center_login"
	// idTokenCookie keeps the raw ID token, only for /auth/ requests: logout passes it to the
	// provider as id_token_hint, so the SSO session ends without a confirmation page.
	idTokenCookie = "argo_center_idt"
	// Browsers drop cookies over 4 KB; a bigger token is skipped and logout asks to confirm.
	maxIDTokenCookie = 3800
	sessionTTL       = 12 * time.Hour
	loginTTL         = 10 * time.Minute

	// csrfHeader must be sent with every mutating API request. Browsers only let same-origin
	// scripts set custom headers, so a cross-site form or link cannot trigger an action.
	csrfHeader = "X-Requested-With"
	csrfValue  = "argo-center"
)

// User is the logged-in person. Name is what the audit log records.
type User struct {
	Subject string `json:"sub"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
}

type Config struct {
	// Disabled turns login off; every request acts as user "local".
	Disabled     bool
	PublicURL    string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// AllowedGroups, when non-empty, admits only users with one of these groups in GroupsClaim.
	AllowedGroups []string
	GroupsClaim   string
	// SessionKey signs cookies; at least 32 bytes.
	SessionKey []byte
}

type Authenticator struct {
	cfg    Config
	secure bool

	// The provider is discovered on first use, so argo-center starts even while the SSO is down.
	mu         sync.Mutex
	oauth      *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string // the provider's end_session_endpoint, if it has one
}

func New(cfg Config) *Authenticator {
	if cfg.Disabled {
		slog.Warn("login is disabled (insecureNoAuth); every request acts as user \"local\"")
	}
	return &Authenticator{cfg: cfg, secure: strings.HasPrefix(cfg.PublicURL, "https://")}
}

func (a *Authenticator) provider(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.oauth != nil {
		return a.oauth, a.verifier, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, a.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	a.oauth = &oauth2.Config{
		ClientID:     a.cfg.ClientID,
		ClientSecret: a.cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.cfg.PublicURL + "/auth/callback",
		Scopes:       a.cfg.Scopes,
	}
	a.verifier = p.Verifier(&oidc.Config{ClientID: a.cfg.ClientID})
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err == nil {
		a.endSession = meta.EndSession
	}
	return a.oauth, a.verifier, nil
}

// Register adds the /auth/ routes and GET /api/me to mux.
func (a *Authenticator) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("GET /auth/signed-out", signedOut)
}

type loginState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Return   string    `json:"return"`
	Expires  time.Time `json:"exp"`
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	ret := safeReturn(r.URL.Query().Get("return"))
	if a.cfg.Disabled {
		http.Redirect(w, r, ret, http.StatusFound)
		return
	}
	oauth, _, err := a.provider(r.Context())
	if err != nil {
		slog.Error("login", "err", err)
		http.Error(w, "The login service is unreachable. Try again shortly.", http.StatusBadGateway)
		return
	}
	st := loginState{
		State:    randomString(),
		Nonce:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Return:   ret,
		Expires:  time.Now().Add(loginTTL),
	}
	a.setCookie(w, loginCookie, "/auth/", st, loginTTL)
	http.Redirect(w, r, oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Disabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	var st loginState
	if !a.readCookie(r, loginCookie, &st) || time.Now().After(st.Expires) || r.URL.Query().Get("state") != st.State {
		http.Error(w, "Login expired or was started in another tab. Go back to argo-center and try again.", http.StatusBadRequest)
		return
	}
	a.clearCookie(w, loginCookie, "/auth/")
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "Login failed: "+e+" "+r.URL.Query().Get("error_description"), http.StatusForbidden)
		return
	}

	user, rawIDToken, err := a.exchange(r.Context(), r.URL.Query().Get("code"), st)
	if err != nil {
		slog.Warn("login rejected", "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, errNotAllowed) {
			status = http.StatusForbidden
		}
		http.Error(w, "Login failed: "+err.Error(), status)
		return
	}
	a.setCookie(w, sessionCookie, "/", session{User: user, Expires: time.Now().Add(sessionTTL)}, sessionTTL)
	if len(rawIDToken) <= maxIDTokenCookie {
		http.SetCookie(w, &http.Cookie{
			Name: idTokenCookie, Value: rawIDToken, Path: "/auth/", MaxAge: int(sessionTTL.Seconds()),
			HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
		})
	}
	slog.Info("login", "user", user.Name)
	http.Redirect(w, r, st.Return, http.StatusFound)
}

var errNotAllowed = errors.New("you are not in a group that may use argo-center")

func (a *Authenticator) exchange(ctx context.Context, code string, st loginState) (User, string, error) {
	oauth, verifier, err := a.provider(ctx)
	if err != nil {
		return User{}, "", err
	}
	tok, err := oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return User{}, "", fmt.Errorf("exchange code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return User{}, "", errors.New("no id_token in token response")
	}
	idt, err := verifier.Verify(ctx, raw)
	if err != nil {
		return User{}, "", fmt.Errorf("verify id_token: %w", err)
	}
	if idt.Nonce != st.Nonce {
		return User{}, "", errors.New("id_token nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return User{}, "", err
	}
	if len(a.cfg.AllowedGroups) > 0 && !slices.ContainsFunc(stringList(claims[a.cfg.GroupsClaim]), func(g string) bool {
		return slices.Contains(a.cfg.AllowedGroups, g)
	}) {
		return User{}, "", errNotAllowed
	}
	u := User{Subject: idt.Subject, Email: str(claims["email"])}
	u.Name = cmp.Or(str(claims["preferred_username"]), u.Email, str(claims["name"]), idt.Subject)
	return u, raw, nil
}

// logout ends the argo-center session and answers with where the browser goes next: the
// provider's end-session endpoint, so the SSO session ends too and the next visit asks for a
// login again, then back to /auth/signed-out.
func (a *Authenticator) logout(w http.ResponseWriter, r *http.Request) {
	a.clearCookie(w, sessionCookie, "/")
	a.clearCookie(w, idTokenCookie, "/auth/")
	next := "/auth/signed-out"
	if !a.cfg.Disabled {
		if _, _, err := a.provider(r.Context()); err == nil && a.endSession != "" {
			q := url.Values{
				"client_id":                {a.cfg.ClientID},
				"post_logout_redirect_uri": {a.cfg.PublicURL + "/auth/signed-out"},
			}
			if c, err := r.Cookie(idTokenCookie); err == nil {
				q.Set("id_token_hint", c.Value)
			}
			next = a.endSession + "?" + q.Encode()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"redirect": next})
}

// signedOut is where the provider sends the browser after logout. It does not log in again on
// its own, unlike the app, which would go straight back to the provider.
func signedOut(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, signedOutPage)
}

const signedOutPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Signed out · argo-center</title>
<style>
:root { color-scheme: light dark; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f5f6f8; color: #16191f;
  font: 14px/1.5 Inter, system-ui, -apple-system, "Segoe UI", sans-serif; }
main { background: #fff; border: 1px solid #e3e6eb; border-radius: 10px; padding: 28px 32px; max-width: 360px; margin: 16px; }
h1 { margin: 0 0 6px; font-size: 18px; }
p { margin: 0 0 18px; color: #5b6472; }
a { display: inline-block; padding: 9px 16px; border-radius: 8px; background: #2457d6; color: #fff; font-weight: 600; text-decoration: none; }
a:hover { background: #1f4bb8; }
@media (prefers-color-scheme: dark) {
  body { background: #141518; color: #e6e8ec; }
  main { background: #1c1e22; border-color: #2d3036; }
  p { color: #a3aab6; }
}
</style>
</head>
<body>
<main>
<h1>You are signed out</h1>
<p>Your argo-center and single sign-on sessions have ended.</p>
<a href="/auth/login">Sign in again</a>
</main>
</body>
</html>
`

type session struct {
	User    User      `json:"user"`
	Expires time.Time `json:"exp"`
}

type ctxKey struct{}

// UserFrom returns the user Middleware attached to the request context.
func UserFrom(ctx context.Context) User {
	u, _ := ctx.Value(ctxKey{}).(User)
	return u
}

// Middleware requires a session for /api/ requests (answering 401 otherwise) and the CSRF header
// for mutating ones. Other paths (the SPA, /auth/, health checks) pass through.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(csrfHeader) != csrfValue {
			writeError(w, http.StatusForbidden, "missing "+csrfHeader+" header")
			return
		}
		user := User{Subject: "local", Name: "local"}
		if !a.cfg.Disabled {
			var s session
			if !a.readCookie(r, sessionCookie, &s) || time.Now().After(s.Expires) {
				writeError(w, http.StatusUnauthorized, "login required")
				return
			}
			user = s.User
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	})
}

// Me serves GET /api/me.
func (a *Authenticator) Me(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		User
		AuthEnabled bool `json:"authEnabled"`
	}{UserFrom(r.Context()), !a.cfg.Disabled})
}

// Cookies are base64url(JSON) + "." + base64url(HMAC-SHA256). They are signed, not encrypted:
// they hold only the user's name and email.

func (a *Authenticator) sign(payload []byte) string {
	mac := hmac.New(sha256.New, a.cfg.SessionKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name, path string, v any, ttl time.Duration) {
	payload, _ := json.Marshal(v)
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: a.sign(payload), Path: path, MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Authenticator) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: path, MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

func (a *Authenticator) readCookie(r *http.Request, name string, v any) bool {
	c, err := r.Cookie(name)
	if err != nil {
		return false
	}
	enc, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(enc)
	got, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.cfg.SessionKey)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return false
	}
	return json.Unmarshal(payload, v) == nil
}

// safeReturn keeps post-login redirects on this site.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) []string {
	switch v := v.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
