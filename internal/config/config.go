// Package config loads argo-center's YAML config and the secrets it references from the environment.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen string `yaml:"listen"`
	// ResyncInterval is how often each instance is fully re-listed to heal any drift in the watch stream.
	ResyncInterval time.Duration `yaml:"resyncInterval"`
	// Clusters are listed in the order they appear in the UI and in rollouts.
	Clusters []Cluster `yaml:"clusters"`

	// PublicURL is where users reach argo-center; the OIDC redirect URL is derived from it.
	PublicURL string `yaml:"publicURL"`
	OIDC      *OIDC  `yaml:"oidc"`
	// SessionKeyEnv names the environment variable holding the base64 key (at least 32 bytes)
	// that signs session cookies.
	SessionKeyEnv string `yaml:"sessionKeyEnv"`
	// InsecureNoAuth turns login off; every request acts as user "local". For local development only.
	InsecureNoAuth bool `yaml:"insecureNoAuth"`

	Storage Storage `yaml:"storage"`
	Rollout Rollout `yaml:"rollout"`

	// SessionKey is resolved from SessionKeyEnv at load time.
	SessionKey []byte `yaml:"-"`
}

type OIDC struct {
	Issuer          string `yaml:"issuer"`
	ClientID        string `yaml:"clientID"`
	ClientSecretEnv string `yaml:"clientSecretEnv"`
	// Scopes default to openid, profile and email.
	Scopes []string `yaml:"scopes"`
	// AllowedGroups, when set, admits only users with at least one of these groups in GroupsClaim.
	AllowedGroups []string `yaml:"allowedGroups"`
	// GroupsClaim is the ID token claim listing the user's groups; defaults to "groups".
	GroupsClaim string `yaml:"groupsClaim"`

	// ClientSecret is resolved from ClientSecretEnv at load time.
	ClientSecret string `yaml:"-"`
}

type Rollout struct {
	// HealthTimeout is how long a rollout waits, per cluster, for the app to become Healthy after
	// its sync; defaults to 10m.
	HealthTimeout time.Duration `yaml:"healthTimeout"`
}

type Storage struct {
	// SQLitePath is the audit log database; defaults to argo-center.db in the working directory.
	SQLitePath string `yaml:"sqlitePath"`
}

type Cluster struct {
	Key         string `yaml:"key"`
	DisplayName string `yaml:"displayName"`
	// URL is the ArgoCD server base URL, e.g. https://argocd.cluster-a.example.com
	URL string `yaml:"url"`
	// TokenEnv names the environment variable holding this instance's ArgoCD API token.
	TokenEnv           string `yaml:"tokenEnv"`
	CAFile             string `yaml:"caFile"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`

	// Token is resolved from TokenEnv at load time.
	Token string `yaml:"-"`
}

var keyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw, os.Getenv)
}

// Parse decodes and validates a config, resolving tokens through getenv.
func Parse(raw []byte, getenv func(string) string) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.ResyncInterval == 0 {
		cfg.ResyncInterval = 5 * time.Minute
	}

	var errs []error
	if len(cfg.Clusters) == 0 {
		errs = append(errs, errors.New("clusters: at least one cluster is required"))
	}
	seen := map[string]bool{}
	for i := range cfg.Clusters {
		c := &cfg.Clusters[i]
		prefix := fmt.Sprintf("clusters[%d]", i)
		if !keyPattern.MatchString(c.Key) {
			errs = append(errs, fmt.Errorf("%s.key %q: must be lowercase letters, digits and dashes", prefix, c.Key))
		} else if seen[c.Key] {
			errs = append(errs, fmt.Errorf("%s.key %q: duplicate", prefix, c.Key))
		}
		seen[c.Key] = true
		if c.DisplayName == "" {
			c.DisplayName = c.Key
		}

		c.URL = strings.TrimRight(c.URL, "/")
		if u, err := url.Parse(c.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s.url %q: must be an absolute http(s) URL", prefix, c.URL))
		}

		if c.TokenEnv == "" {
			errs = append(errs, fmt.Errorf("%s.tokenEnv: required", prefix))
		} else if c.Token = strings.TrimSpace(getenv(c.TokenEnv)); c.Token == "" {
			errs = append(errs, fmt.Errorf("%s: environment variable %s is empty", prefix, c.TokenEnv))
		}
	}
	errs = append(errs, cfg.parseAuth(getenv)...)
	if cfg.Rollout.HealthTimeout == 0 {
		cfg.Rollout.HealthTimeout = 10 * time.Minute
	}
	if cfg.Storage.SQLitePath == "" {
		cfg.Storage.SQLitePath = "argo-center.db"
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *Config) parseAuth(getenv func(string) string) []error {
	switch {
	case cfg.OIDC == nil && cfg.InsecureNoAuth:
		return nil
	case cfg.OIDC == nil:
		return []error{errors.New("oidc: required (set insecureNoAuth: true to run without login, for local development only)")}
	case cfg.InsecureNoAuth:
		return []error{errors.New("insecureNoAuth: cannot be combined with oidc")}
	}

	var errs []error
	o := cfg.OIDC
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("publicURL %q: must be an absolute http(s) URL", cfg.PublicURL))
	}
	if u, err := url.Parse(o.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
		errs = append(errs, fmt.Errorf("oidc.issuer %q: must be an absolute https URL", o.Issuer))
	}
	if o.ClientID == "" {
		errs = append(errs, errors.New("oidc.clientID: required"))
	}
	if o.ClientSecretEnv == "" {
		errs = append(errs, errors.New("oidc.clientSecretEnv: required"))
	} else if o.ClientSecret = strings.TrimSpace(getenv(o.ClientSecretEnv)); o.ClientSecret == "" {
		errs = append(errs, fmt.Errorf("oidc: environment variable %s is empty", o.ClientSecretEnv))
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"openid", "profile", "email"}
	}
	if o.GroupsClaim == "" {
		o.GroupsClaim = "groups"
	}

	if cfg.SessionKeyEnv == "" {
		errs = append(errs, errors.New("sessionKeyEnv: required"))
	} else {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(getenv(cfg.SessionKeyEnv)))
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: not valid base64", cfg.SessionKeyEnv))
		case len(key) < 32:
			errs = append(errs, fmt.Errorf("%s: must decode to at least 32 bytes (openssl rand -base64 32)", cfg.SessionKeyEnv))
		}
		cfg.SessionKey = key
	}
	return errs
}
