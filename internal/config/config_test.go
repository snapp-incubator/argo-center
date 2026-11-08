package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
insecureNoAuth: true
clusters:
  - key: cluster-a
    url: https://argocd.example.io/
    tokenEnv: TOKEN_A
`), env(map[string]string{"TOKEN_A": " secret\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.ResyncInterval != 5*time.Minute || cfg.Storage.SQLitePath != "argo-center.db" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	c := cfg.Clusters[0]
	if c.DisplayName != "cluster-a" || c.URL != "https://argocd.example.io" || c.Token != "secret" {
		t.Errorf("cluster not normalized: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	_, err := Parse([]byte(`
resyncInterval: 1m
clusters:
  - key: Bad_Key
    url: argocd.example.io
    tokenEnv: MISSING
  - key: dup
    url: https://a.example.io
    tokenEnv: T
  - key: dup
    url: https://b.example.io
`), env(map[string]string{"T": "x"}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"Bad_Key", "absolute http(s) URL", "MISSING is empty", "duplicate", "tokenEnv: required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("clusterz: []\n"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "clusterz") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

const oneCluster = `
clusters:
  - key: a
    url: https://argocd.example.io
    tokenEnv: T
`

func TestParseOIDC(t *testing.T) {
	cfg, err := Parse([]byte(`
publicURL: https://argo-center.example.io/
sessionKeyEnv: KEY
oidc:
  issuer: https://sso.example.io/realms/x
  clientID: argo-center
  clientSecretEnv: SECRET
`+oneCluster), env(map[string]string{"T": "x", "SECRET": "s", "KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://argo-center.example.io" || cfg.OIDC.ClientSecret != "s" || len(cfg.SessionKey) != 32 ||
		cfg.OIDC.GroupsClaim != "groups" || len(cfg.OIDC.Scopes) != 3 {
		t.Errorf("oidc config not resolved: %+v %+v", cfg, cfg.OIDC)
	}
}

func TestParseAuthErrors(t *testing.T) {
	_, err := Parse([]byte(oneCluster), env(map[string]string{"T": "x"}))
	if err == nil || !strings.Contains(err.Error(), "insecureNoAuth") {
		t.Errorf("missing oidc: got %v", err)
	}
	_, err = Parse([]byte(`
sessionKeyEnv: KEY
oidc:
  issuer: http://sso.example.io
  clientSecretEnv: SECRET
`+oneCluster), env(map[string]string{"T": "x", "KEY": "c2hvcnQ="}))
	for _, want := range []string{"publicURL", "https URL", "clientID: required", "SECRET is empty", "at least 32 bytes"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}
