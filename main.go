package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robertprast/goop/pkg/proxy"
	openai_proxy "github.com/robertprast/goop/pkg/proxy/openai_schema"
	"github.com/robertprast/goop/pkg/utils"
	"github.com/sirupsen/logrus"
)

// App holds the application configurations and dependencies
type App struct {
	Config           *utils.Config
	Router           *http.ServeMux
	Logger           *logrus.Logger
	Metrics          *proxy.Metrics
	OpenProxyMetrics *proxy.OpenaiProxyMetrics
	Healthy          int32
}

func main() {
	app := &App{
		Logger:           logrus.New(),
		Metrics:          proxy.NewProxyMetrics(),
		OpenProxyMetrics: proxy.NewOpenaiProxyMetrics(),
	}

	// Initialize components
	app.InitLogger()
	app.InitConfig("config.yml")
	app.InitHealth()
	app.InitRouter()

	// Start the server with graceful shutdown
	app.StartServer()
}

// InitLogger sets up the Logrus logger
func (app *App) InitLogger() {
	app.Logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
	app.Logger.SetLevel(logrus.InfoLevel)
}

// InitConfig loads the configuration from a file
func (app *App) InitConfig(configPath string) {
	config, err := utils.LoadConfig(configPath)
	if err != nil {
		app.Logger.Fatalf("Error loading configuration: %v", err)
	}
	app.Config = &config
}

// InitHealth initializes health status
func (app *App) InitHealth() {
	atomic.StoreInt32(&app.Healthy, 1)
}

// InitRouter sets up the HTTP router with all handlers and middleware
func (app *App) InitRouter() {
	mux := http.NewServeMux()

	// Initialize proxy handlers with dependencies
	proxyHandler := proxy.NewProxyHandler(app.Config, app.Logger, app.Metrics)
	openAIProxyHandler := openai_proxy.NewHandler(app.Config, app.Logger, app.OpenProxyMetrics)

	// Define existing routes
	mux.Handle("/", proxyHandler)
	mux.Handle("/openai-proxy/", openAIProxyHandler)

	// Define additional routes
	mux.HandleFunc("/healthz", app.healthHandler)
	mux.Handle("/metrics", promhttp.Handler())

	app.Router = mux
}

// healthHandler handles the /healthz endpoint
func (app *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	status := atomic.LoadInt32(&app.Healthy)
	var response struct {
		Status string `json:"status"`
	}

	if status == 1 {
		response.Status = "healthy"
		w.WriteHeader(http.StatusOK)
	} else {
		response.Status = "unhealthy"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(response)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// StartServer starts the HTTP server and handles graceful shutdown
func (app *App) StartServer() {
	srv := &http.Server{
		Addr:    ":8080",
		Handler: app.Router,
	}

	// Channel to listen for interrupt or terminate signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		app.Logger.Info("Starting engine_proxy server on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			app.Logger.Fatalf("ListenAndServe error: %v", err)
		}
	}()

	// Block until a signal is received
	<-stop

	// Set health to unhealthy
	atomic.StoreInt32(&app.Healthy, 0)

	// Create a deadline to wait for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	app.Logger.Info("Shutting down server...")

	// Attempt graceful shutdown
	if err := srv.Shutdown(ctx); err != nil {
		app.Logger.Fatalf("Server Shutdown Failed:%+v", err)
	}

	app.Logger.Info("Server gracefully stopped")
}
