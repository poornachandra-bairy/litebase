package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/litebase/litebase/internal/api"
	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/config"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/query"
	"github.com/litebase/litebase/internal/rows"
	"github.com/litebase/litebase/internal/scheduler"
	"github.com/litebase/litebase/internal/server"
	"github.com/litebase/litebase/internal/storage"
	"github.com/litebase/litebase/internal/tables"
)

// build constructs every service and wires them together.
//
// Construction order follows the dependency graph: the database manager first,
// since everything else needs the metadata database, then the services that
// read it, and finally the HTTP server.
func build(ctx context.Context, cfg *config.Config, log *slog.Logger) (*app, error) {
	mgr, err := database.NewManager(ctx, database.ManagerOptions{
		DatabasesDir: cfg.DatabasesDir(),
		MetaPath:     cfg.MetaDBPath(),
		MaxOpen:      cfg.MaxOpenDatabases,
	})
	if err != nil {
		return nil, fmt.Errorf("open databases: %w", err)
	}

	authSvc := auth.NewService(mgr.Meta(), cfg.SessionTTL)
	tablesSvc := tables.NewService(mgr)
	rowsSvc := rows.NewService(mgr, tablesSvc, cfg.MaxQueryRows)
	querySvc := query.NewService(mgr, cfg.MaxQueryRows, cfg.QueryTimeout)
	apiSvc := api.NewService(mgr, rowsSvc, querySvc, tablesSvc)

	registry := storage.NewRegistry()
	local, err := storage.NewLocal(cfg.BackupsDir())
	if err != nil {
		mgr.Close()
		return nil, err
	}
	registry.Register(local)

	backupSvc, err := backup.NewService(backup.Options{
		Manager:       mgr,
		Storage:       registry,
		TempDir:       cfg.TempDir(),
		EncryptionKey: cfg.BackupEncryptionKey,
		Logger:        log,
	})
	if err != nil {
		mgr.Close()
		return nil, err
	}

	staticFS, err := resolveStaticFS(cfg, log)
	if err != nil {
		mgr.Close()
		return nil, err
	}

	srv := server.New(server.Options{
		Config:          cfg,
		Logger:          log,
		Manager:         mgr,
		Auth:            authSvc,
		Tables:          tablesSvc,
		Rows:            rowsSvc,
		Query:           querySvc,
		API:             apiSvc,
		Backups:         backupSvc,
		StorageRegistry: registry,
		SettingsSecret:  cfg.BackupEncryptionKey,
		StaticFS:        staticFS,
	})

	a := &app{
		cfg:     cfg,
		log:     log,
		mgr:     mgr,
		auth:    authSvc,
		backups: backupSvc,
		server:  srv,
	}
	a.scheduler = buildScheduler(cfg, log, authSvc, backupSvc)

	// A Google Drive connection saved in an earlier run is restored here so
	// scheduled backups keep working across restarts.
	restoreGoogleDrive(ctx, mgr, registry, cfg, log)
	return a, nil
}

// buildScheduler registers the recurring background jobs.
func buildScheduler(cfg *config.Config, log *slog.Logger, authSvc *auth.Service, backups *backup.Service) *scheduler.Scheduler {
	sched := scheduler.New(log)

	sched.Add(scheduler.Job{
		Name:     "due-backups",
		Interval: cfg.SchedulerInterval,
		Run: func(ctx context.Context) error {
			due, err := backups.DueSchedules(ctx, time.Now())
			if err != nil {
				return err
			}
			for i := range due {
				// One failing schedule must not prevent the others running.
				if err := backups.RunSchedule(ctx, &due[i]); err != nil {
					log.Error("scheduled backup failed", "schedule", due[i].Name, "error", err)
				}
			}
			return nil
		},
	})

	sched.Add(scheduler.Job{
		Name:     "purge-expired-sessions",
		Interval: time.Hour,
		Run: func(ctx context.Context) error {
			n, err := authSvc.PurgeExpiredSessions(ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				log.Debug("purged expired sessions", "count", n)
			}
			return nil
		},
	})
	return sched
}

// resolveStaticFS picks where the dashboard is served from.
func resolveStaticFS(cfg *config.Config, log *slog.Logger) (fs.FS, error) {
	// An explicit directory wins, which is how a packaged build can ship the
	// bundle beside the binary instead of inside it.
	if cfg.StaticDir != "" {
		dir, err := osDirFS(cfg.StaticDir)
		if err != nil {
			return nil, fmt.Errorf("static dir %s: %w", cfg.StaticDir, err)
		}
		log.Info("serving frontend from directory", "dir", cfg.StaticDir)
		return dir, nil
	}

	embedded, err := embeddedFrontend()
	if err != nil {
		// A backend-only build is a legitimate state during development, so
		// this is a notice rather than a failure.
		log.Warn("no dashboard bundle is compiled in; the API is still available",
			"hint", "build the frontend and rebuild, or set LITEBASE_STATIC_DIR")
		return nil, nil
	}
	return embedded, nil
}

func restoreGoogleDrive(ctx context.Context, mgr *database.Manager, registry *storage.Registry, cfg *config.Config, log *slog.Logger) {
	provider, err := server.LoadGoogleDriveProvider(ctx, mgr, cfg.BackupEncryptionKey)
	if err != nil {
		if !errors.Is(err, server.ErrSettingNotFound) {
			log.Warn("could not restore the Google Drive connection", "error", err)
		}
		return
	}
	registry.Register(provider)
	log.Info("google drive storage restored from settings")
}

// serve starts the HTTP server and blocks until shutdown completes.
func (a *app) serve(ctx context.Context) error {
	a.scheduler.Start(ctx)

	errCh := make(chan error, 1)
	go func() { errCh <- a.server.Start() }()

	a.log.Info("litebase started",
		"version", version,
		"addr", a.cfg.Addr,
		"data_dir", a.cfg.DataDir,
		"backups_dir", a.cfg.BackupsDir())

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil

	case <-ctx.Done():
		a.log.Info("shutdown signal received, draining connections",
			"timeout", a.cfg.ShutdownTimeout)
	}

	// Stop background work first so nothing new starts while requests drain.
	a.scheduler.Stop()

	// A fresh context is needed: the parent is already cancelled, and shutdown
	// must be allowed its own grace period.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	if err := a.server.Shutdown(shutdownCtx); err != nil {
		a.log.Error("graceful shutdown timed out; some requests were cut off", "error", err)
		return err
	}
	a.log.Info("shutdown complete")
	return nil
}

// bootstrap creates the first administrator when the instance is empty.
func (a *app) bootstrap(ctx context.Context) error {
	created, generated, err := a.auth.Bootstrap(ctx, a.cfg.BootstrapEmail, a.cfg.BootstrapPassword)
	if err != nil {
		return fmt.Errorf("create the first administrator: %w", err)
	}
	if !created {
		return nil
	}

	email := a.cfg.BootstrapEmail
	if email == "" {
		email = "admin@litebase.local"
	}
	if generated == "" {
		a.log.Info("created the first administrator account", "email", email)
		return nil
	}

	// The generated password is printed once, to stdout rather than the
	// structured log, because it must be readable and copyable and must not be
	// shipped to a log aggregator. The banner is padded so it stands out in a
	// deploy log, which is where a hosted install will read it from.
	fmt.Printf(`

╔══════════════════════════════════════════════════════════════╗
║  LITEBASE - FIRST ADMINISTRATOR ACCOUNT                      ║
╠══════════════════════════════════════════════════════════════╣
║                                                              ║
║   Email:     %-48s║
║   Password:  %-48s║
║                                                              ║
║   Copy this password now. It is stored only as a hash and    ║
║   will not be shown again. Sign in and change it.            ║
║                                                              ║
║   Lost it? Run:  litebase --reset-password <email>           ║
║                                                              ║
╚══════════════════════════════════════════════════════════════╝

`, email, generated)
	return nil
}

// resetPassword implements the --reset-password recovery flag.
func (a *app) resetPassword(ctx context.Context, email string) error {
	user, err := a.auth.FindUserByEmail(ctx, email)
	if err != nil {
		return fmt.Errorf("find %s: %w", email, err)
	}

	password, err := auth.GenerateRecoveryPassword()
	if err != nil {
		return err
	}
	if err := a.auth.SetPassword(ctx, user.ID, password); err != nil {
		return err
	}

	fmt.Printf(`
Password reset for %s

   New password: %s

All existing sessions for this account have been signed out.
`, email, password)
	return nil
}
