// Command litebase runs the Litebase server: a self-hosted SQLite database
// service with a dashboard, a generated REST API and scheduled backups.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/litebase/litebase/internal/auth"
	"github.com/litebase/litebase/internal/backup"
	"github.com/litebase/litebase/internal/config"
	"github.com/litebase/litebase/internal/database"
	"github.com/litebase/litebase/internal/logging"
	"github.com/litebase/litebase/internal/scheduler"
	"github.com/litebase/litebase/internal/server"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "litebase: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to a JSON config file (or set LITEBASE_CONFIG)")
		showVersion = flag.Bool("version", false, "print the version and exit")
		resetEmail  = flag.String("reset-password", "", "reset the password for this email address, then exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("litebase", version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	// Generate and persist an encryption key when none was supplied, so backup
	// encryption works on a default install instead of being silently off.
	generatedKey, err := cfg.EnsureSecret()
	if err != nil {
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.LogFormat)
	if generatedKey {
		log.Info("generated an instance encryption key",
			"path", cfg.SecretPath(),
			"note", "keep this file; without it existing encrypted backups cannot be restored")
	}
	server.Version = version

	// A root context cancelled on SIGINT/SIGTERM drives graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer application.Close()

	if *resetEmail != "" {
		return application.resetPassword(ctx, *resetEmail)
	}

	if err := application.bootstrap(ctx); err != nil {
		return err
	}
	return application.serve(ctx)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Litebase - a self-hosted SQLite database service.

Usage:
  litebase [flags]

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Configuration is read from a JSON config file and the environment; see
.env.example for every supported variable. All data is written under the
data directory (LITEBASE_DATA_DIR, default ./data).
`)
}

// app holds the constructed services so startup and shutdown stay symmetrical.
type app struct {
	cfg       *config.Config
	log       *slog.Logger
	mgr       *database.Manager
	auth      *auth.Service
	backups   *backup.Service
	scheduler *scheduler.Scheduler
	server    *server.Server
}

func (a *app) Close() {
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.mgr != nil {
		if err := a.mgr.Close(); err != nil {
			a.log.Error("close databases", "error", err)
		}
	}
}
