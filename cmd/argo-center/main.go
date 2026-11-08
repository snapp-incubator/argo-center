package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/snapp-incubator/argo-center/internal/api"
	"github.com/snapp-incubator/argo-center/internal/argocd"
	"github.com/snapp-incubator/argo-center/internal/audit"
	"github.com/snapp-incubator/argo-center/internal/auth"
	"github.com/snapp-incubator/argo-center/internal/cache"
	"github.com/snapp-incubator/argo-center/internal/config"
	"github.com/snapp-incubator/argo-center/internal/rollout"
	"github.com/snapp-incubator/argo-center/internal/store"
	"github.com/snapp-incubator/argo-center/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the config file")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(*configPath); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	sources := make([]cache.Source, 0, len(cfg.Clusters))
	for _, cl := range cfg.Clusters {
		client, err := argocd.New(argocd.Options{
			BaseURL:            cl.URL,
			Token:              cl.Token,
			CAFile:             cl.CAFile,
			InsecureSkipVerify: cl.InsecureSkipVerify,
		})
		if err != nil {
			return err
		}
		sources = append(sources, cache.Source{Key: cl.Key, DisplayName: cl.DisplayName, URL: cl.URL, Client: client})
	}

	static, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(cfg.Storage.SQLitePath)
	if err != nil {
		return err
	}
	defer db.Close()
	auditLog, err := audit.New(db)
	if err != nil {
		return err
	}

	authCfg := auth.Config{Disabled: cfg.InsecureNoAuth, PublicURL: cfg.PublicURL, SessionKey: cfg.SessionKey}
	if o := cfg.OIDC; o != nil {
		authCfg.Issuer, authCfg.ClientID, authCfg.ClientSecret = o.Issuer, o.ClientID, o.ClientSecret
		authCfg.Scopes, authCfg.AllowedGroups, authCfg.GroupsClaim = o.Scopes, o.AllowedGroups, o.GroupsClaim
	}
	authn := auth.New(authCfg)

	c := cache.New(sources, cfg.ResyncInterval)
	go c.Run(ctx)

	rollouts, err := rollout.New(db, c, auditLog, cfg.Rollout.HealthTimeout)
	if err != nil {
		return err
	}
	if err := rollouts.Start(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(c, static, authn, auditLog, rollouts),
		ReadHeaderTimeout: 10 * time.Second,
		// Request contexts end on shutdown, which closes open SSE streams.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("listening", "addr", cfg.Listen, "clusters", len(sources))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
