/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/markdr-hue/HO/brain"
	"github.com/markdr-hue/HO/chat"
	"github.com/markdr-hue/HO/config"
	"github.com/markdr-hue/HO/db"
	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/llm"
	"github.com/markdr-hue/HO/security"
	"github.com/markdr-hue/HO/server/admin"
	"github.com/markdr-hue/HO/server/middleware"
	"github.com/markdr-hue/HO/server/public"
	"github.com/markdr-hue/HO/tools"
)

// ServerDeps holds all dependencies needed by the HTTP servers.
type ServerDeps struct {
	Config          *config.Config
	DB              *db.DB
	SiteDBManager   *db.SiteDBManager
	JWTManager      *security.JWTManager
	Encryptor       *security.Encryptor
	Bus             *events.Bus
	BrainManager    *brain.BrainManager
	ChatHandler     *chat.ChatHandler
	LLMRegistry     *llm.Registry
	ToolRegistry    *tools.Registry
	ProviderFactory llm.ProviderFactory
	Logger          *slog.Logger
	AdminFS         fs.FS // embedded admin SPA filesystem
	Version         string
}

// Server manages the admin and public HTTP servers.
type Server struct {
	adminRouter  chi.Router
	publicRouter chi.Router
	deps         *ServerDeps
}

// New creates and configures both routers.
func New(deps *ServerDeps) *Server {
	s := &Server{deps: deps}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// ---- Admin Router ----
	// No rate limiting on admin - it's a local admin tool, not a public API.
	s.adminRouter = chi.NewRouter()
	s.adminRouter.Use(middleware.RequestID)
	s.adminRouter.Use(middleware.Logging(logger.With("server", "admin")))
	adminCORS := deps.Config.CORSOrigins
	if len(adminCORS) == 0 {
		adminCORS = []string{
			fmt.Sprintf("http://localhost:%d", deps.Config.AdminPort),
			fmt.Sprintf("http://127.0.0.1:%d", deps.Config.AdminPort),
		}
	}
	s.adminRouter.Use(middleware.CORS(adminCORS))
	s.adminRouter.Use(middleware.SecurityHeaders)
	s.adminRouter.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://esm.sh; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; img-src 'self' data:; connect-src 'self' https://esm.sh; font-src 'self' https://fonts.gstatic.com")
			next.ServeHTTP(w, r)
		})
	})

	adminDeps := &admin.Deps{
		Config:          deps.Config,
		DB:              deps.DB,
		SiteDBManager:   deps.SiteDBManager,
		JWTManager:      deps.JWTManager,
		Encryptor:       deps.Encryptor,
		Bus:             deps.Bus,
		BrainManager:    deps.BrainManager,
		ChatHandler:     deps.ChatHandler,
		LLMRegistry:     deps.LLMRegistry,
		ToolRegistry:    deps.ToolRegistry,
		ProviderFactory: deps.ProviderFactory,
		Logger:          logger.With("server", "admin"),
		Version:         deps.Version,
	}
	admin.RegisterRoutes(s.adminRouter, adminDeps, deps.AdminFS)

	// ---- Public Router ----
	publicRateLimiter := middleware.NewRateLimiter(deps.Config.RateLimitRate, deps.Config.RateLimitBurst)
	s.publicRouter = chi.NewRouter()
	s.publicRouter.Use(middleware.RequestID)
	s.publicRouter.Use(middleware.Logging(logger.With("server", "public")))
	// Wildcard CORS: public sites serve user-configured domains we can't predict.
	s.publicRouter.Use(middleware.CORS([]string{"*"}))
	s.publicRouter.Use(publicRateLimiter.Handler)
	s.publicRouter.Use(middleware.SecurityHeaders)

	publicDeps := &public.Deps{
		DB:            deps.DB,
		SiteDBManager: deps.SiteDBManager,
		Bus:           deps.Bus,
		WSHub:         public.NewWSHub(),
		LLMRegistry:   deps.LLMRegistry,
		Encryptor:     deps.Encryptor,
		JWTManager:    deps.JWTManager,
		Logger:        logger.With("server", "public"),
	}
	public.RegisterRoutes(s.publicRouter, publicDeps)

	return s
}

// Start starts both HTTP servers in goroutines. It blocks until the provided
// context is cancelled or an OS signal (SIGINT, SIGTERM) is received, then
// performs graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	adminAddr := fmt.Sprintf(":%d", s.deps.Config.AdminPort)
	publicAddr := fmt.Sprintf(":%d", s.deps.Config.PublicPort)

	adminServer := &http.Server{
		Addr:         adminAddr,
		Handler:      s.adminRouter,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	publicServer := &http.Server{
		Addr:         publicAddr,
		Handler:      s.publicRouter,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		s.deps.Logger.Debug("admin server starting", "addr", adminAddr)
		if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("admin server: %w", err)
		}
	}()

	go func() {
		s.deps.Logger.Debug("public server starting", "addr", publicAddr)
		if err := publicServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("public server: %w", err)
		}
	}()

	// Wait for context cancellation, OS signal, or server error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		s.deps.Logger.Info("context cancelled, shutting down servers")
	case sig := <-sigCh:
		s.deps.Logger.Info("received signal, shutting down servers", "signal", sig)
	case err := <-errCh:
		s.deps.Logger.Error("server error, shutting down", "error", err)
		return err
	}

	// Graceful shutdown with timeout.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var shutdownErr error
	if err := adminServer.Shutdown(shutdownCtx); err != nil {
		s.deps.Logger.Error("admin server shutdown error", "error", err)
		shutdownErr = err
	}
	if err := publicServer.Shutdown(shutdownCtx); err != nil {
		s.deps.Logger.Error("public server shutdown error", "error", err)
		shutdownErr = err
	}

	s.deps.Logger.Info("servers stopped")
	return shutdownErr
}
