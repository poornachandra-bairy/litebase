// Package server wires the HTTP layer: routing, middleware and handlers.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/api"
	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/config"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/middleware"
	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/storage"
	"github.com/litebase/litebase/internal/tables"
)

// Server owns the HTTP listener and the services behind it.
type Server struct {
	cfg *config.Config
	log *slog.Logger

	mgr     *database.Manager
	auth    *auth.Service
	tables  *tables.Service
	rows    *rows.Service
	query   *query.Service
	api     *api.Service
	backups *backup.Service

	settings        *settingsStore
	storageRegistry *storage.Registry

	// Separate limiters keep an aggressive data-API client from exhausting the
	// dashboard's quota, and give login attempts a much tighter budget.
	adminLimiter *middleware.RateLimiter
	apiLimiter   *middleware.RateLimiter
	authLimiter  *middleware.RateLimiter

	staticFS fs.FS
	http     *http.Server
	started  time.Time
}

// Options carries the dependencies a Server needs.
type Options struct {
	Config  *config.Config
	Logger  *slog.Logger
	Manager *database.Manager
	Auth    *auth.Service
	Tables  *tables.Service
	Rows    *rows.Service
	Query   *query.Service
	API     *api.Service
	Backups *backup.Service
	// StorageRegistry lets Settings register a provider configured at runtime.
	StorageRegistry *storage.Registry
	// SettingsSecret encrypts secret settings at rest.
	SettingsSecret string
	// StaticFS serves the compiled frontend. A nil value disables it, which is
	// what the dev setup does while Vite serves the UI itself.
	StaticFS fs.FS
}

// New builds a Server and its route table.
func New(o Options) *Server {
	s := &Server{
		cfg:             o.Config,
		log:             o.Logger,
		mgr:             o.Manager,
		auth:            o.Auth,
		tables:          o.Tables,
		rows:            o.Rows,
		query:           o.Query,
		api:             o.API,
		backups:         o.Backups,
		staticFS:        o.StaticFS,
		storageRegistry: o.StorageRegistry,
		settings: &settingsStore{
			db:     o.Manager.MetaDB(),
			readDB: o.Manager.MetaRead(),
			secret: o.SettingsSecret,
		},
		adminLimiter: middleware.NewRateLimiter(o.Config.AdminRatePerMin),
		apiLimiter:   middleware.NewRateLimiter(o.Config.APIRatePerMin),
		authLimiter:  middleware.NewRateLimiter(o.Config.AuthRatePerMin),
		started:      time.Now(),
	}

	handler := middleware.Chain(s.routes(),
		middleware.RequestID(s.log),
		middleware.RequestLogger(),
		middleware.Recover(),
		middleware.SecureHeaders(o.Config.DevMode),
		middleware.CORS(o.Config.AllowedOrigins),
	)

	s.http = &http.Server{
		Addr:         o.Config.Addr,
		Handler:      handler,
		ReadTimeout:  o.Config.ReadTimeout,
		WriteTimeout: o.Config.WriteTimeout,
		IdleTimeout:  90 * time.Second,
		// Header reading has its own short deadline so a slow-header attack
		// cannot hold a connection open for the full read timeout.
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	return s
}

// Start begins serving and blocks until the listener stops.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}
	s.log.Info("http server listening", "addr", ln.Addr().String())

	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting connections and waits for in-flight requests,
// bounded by the context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	s.adminLimiter.Close()
	s.apiLimiter.Close()
	s.authLimiter.Close()
	return err
}

// Handler exposes the fully wrapped handler for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }
