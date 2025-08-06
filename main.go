package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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

	// Update logger based on config
	app.updateLoggerFromConfig()
}

// updateLoggerFromConfig updates logger settings from configuration
func (app *App) updateLoggerFromConfig() {
	// Set log level
	switch strings.ToLower(app.Config.Logging.Level) {
	case "trace":
		app.Logger.SetLevel(logrus.TraceLevel)
	case "debug":
		app.Logger.SetLevel(logrus.DebugLevel)
	case "info":
		app.Logger.SetLevel(logrus.InfoLevel)
	case "warn":
		app.Logger.SetLevel(logrus.WarnLevel)
	case "error":
		app.Logger.SetLevel(logrus.ErrorLevel)
	case "fatal":
		app.Logger.SetLevel(logrus.FatalLevel)
	case "panic":
		app.Logger.SetLevel(logrus.PanicLevel)
	}

	// Set log format
	if strings.ToLower(app.Config.Logging.Format) == "json" {
		app.Logger.SetFormatter(&logrus.JSONFormatter{})
	} else {
		app.Logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})
	}

	// Add environment info in production
	if app.Config.IsProduction() {
		app.Logger.SetFormatter(&logrus.JSONFormatter{
			FieldMap: logrus.FieldMap{
				logrus.FieldKeyTime:  "timestamp",
				logrus.FieldKeyLevel: "level",
				logrus.FieldKeyMsg:   "message",
			},
		})
	}
}

// InitDatabase initializes the database connection
func (app *App) InitDatabase() {
	// Use database URL from config (which handles env vars)
	databaseURL := app.Config.DatabaseURL

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
	if app.Config.Auth.Disabled {
		app.Logger.Info("Authentication is disabled by configuration")
		return
	}

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

	// Log available engines
	app.logAvailableEngines()

	// Apply auth middleware to protected endpoints if auth is available
	if app.AuthMiddleware != nil {
		mux.HandleFunc("/", app.AuthMiddleware.RequireAuth(proxyHandler.ServeHTTP))
		mux.HandleFunc("/openai-proxy/", app.AuthMiddleware.RequireAuth(openAIProxyHandler.ServeHTTP))

		// Admin endpoints require admin role
		adminHandler := auth.NewHandler(app.AuthService, app.Logger)
		mux.HandleFunc("/admin/keys/", app.AuthMiddleware.RequireAdminAuth(adminHandler.ServeHTTP))
		mux.HandleFunc("/admin/keys", app.AuthMiddleware.RequireAdminAuth(adminHandler.ServeHTTP))
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

	// Use port from config
	port := strconv.Itoa(app.Config.Server.Port)

	srv := &http.Server{
		Addr:         app.Config.Server.Host + ":" + port,
		Handler:      app.Router,
		ReadTimeout:  time.Duration(app.Config.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(app.Config.Server.WriteTimeout) * time.Second,
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(app.Config.Server.ShutdownTimeout)*time.Second)
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

// logAvailableEngines logs which engines are available based on their credentials
func (app *App) logAvailableEngines() {
	// Create a temporary engine cache to check availability
	engineCache := proxy.NewEngineCache(app.Config, app.Logger)
	availableEngines := engineCache.GetAvailableEngines()

	if len(availableEngines) == 0 {
		app.Logger.Warn("No engines available - check your API key configuration")
		return
	}

	app.Logger.Infof("Available engines: %v", availableEngines)

	// Log which engines are disabled and why
	allEngines := []string{"openai", "gemini", "bedrock"}
	for _, engine := range allEngines {
		found := false
		for _, available := range availableEngines {
			if available == engine {
				found = true
				break
			}
		}
		if !found {
			if _, exists := app.Config.Engines[engine]; !exists {
				app.Logger.Infof("Engine %s: not configured", engine)
			} else {
				app.Logger.Infof("Engine %s: configured but missing credentials", engine)
			}
		}
	}
}
