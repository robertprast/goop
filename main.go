package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robertprast/goop/pkg/auth"
	"github.com/robertprast/goop/pkg/proxy"
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
	DB               *pgxpool.Pool
	AuthService      *auth.Service
	AuthMiddleware   *auth.Middleware
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
	app.InitDatabase()
	app.InitAuth()
	app.InitHealth()
	app.InitRouter()

	// Start the server and block until it shuts down.
	if err := app.StartServer(); err != nil {
		app.Logger.Fatalf("Server failed to start or shut down gracefully: %v", err)
	}

	app.Logger.Info("Server has been stopped gracefully!")
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

// InitDatabase initializes the database connection
func (app *App) InitDatabase() {
	// Get database URL from environment variable or config
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" && app.Config.DatabaseURL != "" {
		databaseURL = app.Config.DatabaseURL
	}

	if databaseURL == "" {
		app.Logger.Warn("No database URL configured, auth will be disabled")
		return
	}

	db, err := auth.InitDB(context.Background(), databaseURL)
	if err != nil {
		app.Logger.Fatalf("Error initializing database: %v", err)
	}
	app.DB = db
}

// InitAuth initializes the authentication service and middleware
func (app *App) InitAuth() {
	if app.DB == nil {
		app.Logger.Warn("Database not initialized, auth will be disabled")
		return
	}

	app.AuthService = auth.NewService(app.DB)
	app.AuthMiddleware = auth.NewMiddleware(app.AuthService, app.Logger)
}

// InitHealth initializes health status
func (app *App) InitHealth() {
	atomic.StoreInt32(&app.Healthy, 1)
}

// InitRouter sets up the HTTP router with all handlers and middleware
func (app *App) InitRouter() {
	mux := http.NewServeMux()

	proxyHandler := proxy.NewProxyHandler(app.Config, app.Logger, app.Metrics)
	openAIProxyHandler := proxy.NewHandler(app.Config, app.Logger, app.OpenProxyMetrics)

	// Apply auth middleware to protected endpoints if auth is available
	if app.AuthMiddleware != nil {
		mux.HandleFunc("/", app.AuthMiddleware.RequireAuth(proxyHandler.ServeHTTP))
		mux.HandleFunc("/openai-proxy/", app.AuthMiddleware.RequireAuth(openAIProxyHandler.ServeHTTP))

		// Admin endpoints require admin role
		adminHandler := auth.NewHandler(app.AuthService, app.Logger)
		mux.HandleFunc("/admin/keys/", app.AuthMiddleware.RequireAdminAuth(adminHandler.ServeHTTP))
		mux.HandleFunc("/admin/keys", app.AuthMiddleware.RequireAdminAuth(adminHandler.ServeHTTP))
	} else {
		// No auth - endpoints are unprotected
		mux.Handle("/", proxyHandler)
		mux.Handle("/openai-proxy/", openAIProxyHandler)
	}

	// Health and metrics endpoints are always unprotected
	mux.HandleFunc("/healthz", app.healthHandler)
	mux.Handle("/metrics", promhttp.Handler())

	app.Router = mux
}

// healthHandler handles the /healthz endpoint
func (app *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	// Set health to unhealthy during shutdown to prevent new requests
	if atomic.LoadInt32(&app.Healthy) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	response := struct {
		Status string `json:"status"`
	}{
		Status: "healthy",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Log the error but don't try to write another header
		app.Logger.Errorf("Error writing healthz response: %v", err)
	}
}

// StartServer starts the HTTP server and handles graceful shutdown.
func (app *App) StartServer() error {
	// Channel to listen for OS signals for shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Determine port from environment or default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: app.Router,
	}

	// Channel to listen for server errors
	serverErrors := make(chan error, 1)

	// Start the server in a goroutine
	go func() {
		app.Logger.Infof("Starting server on port %s", port)
		serverErrors <- srv.ListenAndServe()
	}()

	// Block until we receive a shutdown signal or a server error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case sig := <-stop:
		app.Logger.Infof("Shutdown signal received: %s", sig)

		// Set health status to unhealthy to stop receiving new traffic
		atomic.StoreInt32(&app.Healthy, 0)
		app.Logger.Info("Health status set to unhealthy.")

		// Create a context with a timeout for graceful shutdown.
		// Lambda's shutdown phase is short, so this timeout should be brief.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Attempt to gracefully shut down the server
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}

		// Close database connection if it exists
		if app.DB != nil {
			app.DB.Close()
			app.Logger.Info("Database connection closed")
		}
	}

	return nil
}
