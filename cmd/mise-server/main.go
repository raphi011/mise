// Command mise-server runs the mise web app.
//
//	mise-server [serve] [flags]   run migrations and serve HTTP (default)
//	mise-server migrate [flags]   run migrations and exit
//	mise-server backup <dir> [flags]
//	                              archive the SQLite database and the images
//	                              into <dir> (docs/operations.md)
//	mise-server version           print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
	_ "time/tzdata" // household time zones must resolve in minimal containers

	"github.com/raphi011/mise/internal/backup"
	"github.com/raphi011/mise/internal/config"
	"github.com/raphi011/mise/internal/jobs"
	"github.com/raphi011/mise/internal/store"
	"github.com/raphi011/mise/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = ""

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "mise-server:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		_, err := fmt.Fprintln(stdout, versionString())
		return err
	case "serve", "migrate":
	case "backup":
		// config.Load takes only flags: the directory comes first.
		if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
			return errors.New("usage: mise-server backup <dir> [flags]")
		}
		return runBackup(ctx, args[0], args[1:], getenv, stderr)
	default:
		return fmt.Errorf("unknown command %q (want serve, migrate, backup or version)", cmd)
	}

	cfg, err := config.Load(args, getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg, stderr)

	db, err := store.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	n, err := db.Migrate(ctx)
	if err != nil {
		return err
	}
	log.Info("database ready", slog.String("dialect", db.Dialect().String()), slog.Int("migrations_applied", n))
	if cmd == "migrate" {
		return nil
	}
	return serve(ctx, cfg, db, log)
}

// runBackup writes a backup archive into dir. It does not run migrations:
// the backup holds the database as it is.
func runBackup(ctx context.Context, dir string, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := config.Load(args, getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg, stderr)

	// A missing SQLite file means a wrong MISE_DB or MISE_DATA_DIR; creating
	// it would back up an empty database and exit 0.
	db, err := store.OpenExisting(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	res, err := backup.Write(ctx, db, cfg.DataDir, dir, time.Now())
	if err != nil {
		return err
	}
	log.Info("backup written", slog.String("path", res.Path),
		slog.Bool("database", res.Database), slog.Int("images", res.Images))
	if !res.Database {
		log.Warn("the archive holds only the images: back up the Postgres database with pg_dump, see docs/operations.md")
	}
	return nil
}

func serve(ctx context.Context, cfg config.Config, db *store.DB, log *slog.Logger) error {
	server, err := web.New(cfg, db, log)
	if err != nil {
		return err
	}
	if err := server.LoadSettings(ctx); err != nil {
		return err
	}
	if err := server.SeedGroceries(ctx); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// SSE handlers clear this per stream with http.ResponseController.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  2 * time.Minute,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// Live streams only end with their request; end them on shutdown so it
	// does not wait for open tabs.
	srv.RegisterOnShutdown(server.CloseStreams)

	// Periodic maintenance stops with ctx.
	go jobs.NewSweeper(db, log, nil, 0).WithImages(server.Images()).Run(ctx)

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", slog.String("addr", cfg.Listen), slog.String("version", versionString()))
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	// Reset emails go out after their response; let them finish.
	server.Wait()
	return nil
}

func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func versionString() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
