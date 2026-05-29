// Command goop runs the LLM reverse proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/robertprast/goop/internal/auth"
	"github.com/robertprast/goop/internal/config"
	"github.com/robertprast/goop/internal/models"
	"github.com/robertprast/goop/internal/provider"
	"github.com/robertprast/goop/internal/proxy"
	"github.com/robertprast/goop/internal/router"
	"github.com/robertprast/goop/internal/translator"
)

func main() {
	configPath := flag.String("config", "config.yml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("config", err)
	}

	logger := newLogger(cfg.Logging)
	slog.SetDefault(logger)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	providers, warnings, err := provider.BuildAll(rootCtx, cfg)
	if err != nil {
		fatal("providers", err)
	}
	for _, w := range warnings {
		logger.Info(w)
	}
	if len(providers) == 0 {
		logger.Warn("no providers configured — only /healthz will respond")
	}

	registry := provider.NewRegistry(providers...)
	for _, p := range registry.All() {
		logger.Info("provider enabled", "name", p.Name(), "prefix", p.Prefix())
	}

	agg := models.New(registry, cfg.Models.TTL, logger)
	// 10 MiB matches the translator's cap; large enough for any reasonable
	// chat payload but small enough to keep a streamed-POST attacker from
	// exhausting memory in the provider transports.
	srv := proxy.NewWithLimit(registry, agg, logger, 10<<20)

	mw := auth.Middleware(cfg.Auth.APIKey)

	// Authenticated routes go through the auth middleware. /healthz is
	// served unauthenticated so liveness probes don't need the bearer.
	authed := http.NewServeMux()
	authed.Handle("/", srv.Handler())
	var bedrockTranslator http.Handler
	if bp := registry.ByName("bedrock"); bp != nil {
		bh := translator.NewBedrockHandler(bp, proxy.DefaultTransport(), logger)
		bedrockTranslator = bh
		// /openai-bedrock/... is the legacy alias; /bedrock-translate/... is
		// the preferred name (the path made people think it was an OpenAI-
		// compat passthrough rather than the Converse translator).
		authed.Handle("/bedrock-translate/v1/chat/completions", bh)
		authed.Handle("/openai-bedrock/v1/chat/completions", bh)

		// Some OpenAI-shape clients (open-webui, LiteLLM) probe /v1/models
		// when validating a connection. Without this, the translator's URL
		// space looks half-broken: chat works but the connection itself
		// shows as unhealthy in the UI. Reuse the native bedrock catalog.
		mh := translator.NewModelsHandler(bp, logger)
		authed.Handle("/bedrock-translate/v1/models", mh)
		authed.Handle("/openai-bedrock/v1/models", mh)
	}

	// Smart top-level OpenAI endpoint. Clients can POST to /v1/chat/completions
	// with a goop-namespaced model ID (e.g. "together/moonshotai/Kimi-K2.6")
	// and the router dispatches to the right provider — no static model_list
	// in front of goop required. The path is registered above the catch-all
	// "/" so http.ServeMux's longest-match wins.
	smart := router.NewResolver(registry, bedrockTranslator, logger).Handler(srv.Handler())
	authed.Handle("/v1/chat/completions", smart)

	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	root.Handle("/", mw(authed))
	handler := http.Handler(root)

	httpSrv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	if err := run(httpSrv, cfg.Server.ShutdownTimeout, logger); err != nil {
		fatal("server", err)
	}
}

func run(srv *http.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errs := make(chan error, 1)
	go func() {
		logger.Info("starting", "addr", srv.Addr)
		errs <- srv.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case sig := <-stop:
		logger.Info("shutdown signal", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
	}
	logger.Info("stopped")
	return nil
}

func newLogger(cfg config.Logging) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.Format, "json") {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func fatal(component string, err error) {
	slog.Error("fatal", "component", component, "err", err)
	os.Exit(1)
}
