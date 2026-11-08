package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// fakeIssuer is a minimal OIDC provider that approves every login as the given claims.
func fakeIssuer(t *testing.T, claims map[string]any) *httptest.Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	codes := map[string]string{} // code -> nonce
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize", "token_endpoint": srv.URL + "/token",
				"jwks_uri": srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
				"end_session_endpoint": srv.URL + "/logout",
			})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("code_challenge_method") != "S256" {
				t.Error("login without PKCE")
			}
			codes["c1"] = q.Get("nonce")
			http.Redirect(w, r, q.Get("redirect_uri")+"?code=c1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
		case "/token":
			r.ParseForm()
			if r.Form.Get("code_verifier") == "" {
				t.Error("token exchange without code_verifier")
			}
			c := map[string]any{"iss": srv.URL, "aud": "argo-center", "sub": "u1", "exp": time.Now().Add(time.Hour).Unix(),
				"iat": time.Now().Unix(), "nonce": codes[r.Form.Get("code")]}
			for k, v := range claims {
				c[k] = v
			}
			payload, _ := json.Marshal(c)
			jws, _ := signer.Sign(payload)
			idToken, _ := jws.CompactSerialize()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idToken})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func newApp(t *testing.T, cfg Config) (*httptest.Server, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	var a *Authenticator
	var app *httptest.Server
	app = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.Middleware(mux).ServeHTTP(w, r) }))
	cfg.PublicURL = app.URL
	cfg.ClientID = "argo-center"
	cfg.ClientSecret = "s"
	cfg.Scopes = []string{"openid"}
	cfg.GroupsClaim = "groups"
	cfg.SessionKey = []byte("0123456789abcdef0123456789abcdef")
	a = New(cfg)
	a.Register(mux)
	mux.HandleFunc("GET /api/me", a.Me)
	mux.HandleFunc("POST /api/act", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, UserFrom(r.Context()).Name) })
	jar, _ := cookiejar.New(nil)
	return app, &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestLoginFlow(t *testing.T) {
	idp := fakeIssuer(t, map[string]any{"preferred_username": "ali", "email": "ali@example.com", "groups": []string{"cloud-team"}})
	defer idp.Close()
	app, client := newApp(t, Config{Issuer: idp.URL, AllowedGroups: []string{"cloud-team"}})
	defer app.Close()

	if code, _ := get(t, client, app.URL+"/api/me"); code != http.StatusUnauthorized {
		t.Fatalf("/api/me before login: %d", code)
	}

	// The client follows login -> provider -> callback -> return path.
	resp, err := client.Get(app.URL + "/auth/login?return=/api/me")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"name":"ali"`) || !strings.Contains(string(body), `"authEnabled":true`) {
		t.Fatalf("after login: %d %s", resp.StatusCode, body)
	}

	req, _ := http.NewRequest("POST", app.URL+"/api/act", nil)
	if resp, _ := client.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without CSRF header: %d", resp.StatusCode)
	}
	req.Header.Set("X-Requested-With", "argo-center")
	resp, _ = client.Do(req)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ali" {
		t.Errorf("POST with header: %d %s", resp.StatusCode, body)
	}

	// A tampered cookie is rejected.
	u, _ := url.Parse(app.URL)
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			c.Value = strings.Replace(c.Value, ".", "x.", 1)
			client.Jar.SetCookies(u, []*http.Cookie{c})
		}
	}
	if code, _ := get(t, client, app.URL+"/api/me"); code != http.StatusUnauthorized {
		t.Errorf("tampered cookie accepted: %d", code)
	}
}

func TestLogoutEndsSSOSession(t *testing.T) {
	idp := fakeIssuer(t, map[string]any{"preferred_username": "ali"})
	defer idp.Close()
	app, client := newApp(t, Config{Issuer: idp.URL})
	defer app.Close()
	if code, _ := get(t, client, app.URL+"/auth/login?return=/api/me"); code != 200 {
		t.Fatalf("login: %d", code)
	}

	resp, err := client.Post(app.URL+"/auth/logout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Redirect string }
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	next, err := url.Parse(out.Redirect)
	if err != nil || !strings.HasPrefix(out.Redirect, idp.URL+"/logout?") {
		t.Fatalf("logout redirect: %q", out.Redirect)
	}
	q := next.Query()
	if q.Get("client_id") != "argo-center" || q.Get("post_logout_redirect_uri") != app.URL+"/auth/signed-out" {
		t.Errorf("logout query: %v", q)
	}
	if strings.Count(q.Get("id_token_hint"), ".") != 2 {
		t.Errorf("id_token_hint missing: %q", q.Get("id_token_hint"))
	}

	if code, _ := get(t, client, app.URL+"/api/me"); code != http.StatusUnauthorized {
		t.Errorf("/api/me after logout: %d", code)
	}
	if code, body := get(t, client, app.URL+"/auth/signed-out"); code != 200 || !strings.Contains(body, "signed out") {
		t.Errorf("signed-out page: %d", code)
	}
}

func TestLoginRejectsOtherGroups(t *testing.T) {
	idp := fakeIssuer(t, map[string]any{"preferred_username": "x", "groups": []string{"marketing"}})
	defer idp.Close()
	app, client := newApp(t, Config{Issuer: idp.URL, AllowedGroups: []string{"cloud-team"}})
	defer app.Close()
	if code, body := get(t, client, app.URL+"/auth/login"); code != http.StatusForbidden || !strings.Contains(body, "not in a group") {
		t.Errorf("got %d %s", code, body)
	}
}

func TestDisabled(t *testing.T) {
	app, client := newApp(t, Config{Disabled: true})
	defer app.Close()
	if code, body := get(t, client, app.URL+"/api/me"); code != 200 || !strings.Contains(body, `"name":"local"`) {
		t.Errorf("got %d %s", code, body)
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{"/apps/x": "/apps/x", "//evil.com": "/", "https://evil.com": "/", "": "/", "/\\evil": "/"} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q", in, got)
		}
	}
}
