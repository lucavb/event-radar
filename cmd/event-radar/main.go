package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lucabecker/event-radar/internal/observ"
	"github.com/lucabecker/event-radar/internal/radar"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "run"
	if len(args) > 0 && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "render output without sending notifications")
	if err := flags.Parse(args); err != nil {
		return err
	}

	config, err := radar.LoadConfig()
	if err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if err := setLogger(config); err != nil {
		return err
	}
	traceShutdown, err := observ.SetupTracing(context.Background(), config.TracingEnabled == "true")
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = traceShutdown(shutdownCtx)
	}()
	store, err := radar.OpenStore(config.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	app := radar.New(config, store, radar.SourcesFromConfig(config))
	ctx := context.Background()

	switch command {
	case "check-config":
		fmt.Println("configuration is valid")
		return nil
	case "sync":
		return app.Sync(ctx)
	case "digest":
		if err := app.Sync(ctx); err != nil {
			slog.WarnContext(ctx, "sync warning", "error", err)
		}
		events, err := app.UpcomingEvents(ctx)
		if err != nil {
			return err
		}
		content := radar.BuildDigest(config, events, time.Now())
		fmt.Print(content)
		changed, err := store.DeliveryChanged(ctx, "email", radar.DigestHash(content))
		if err != nil {
			return err
		}
		if !changed {
			fmt.Println("Digest unchanged; not sending.")
			return nil
		}
		if err := radar.SendDigest(ctx, config, content, *dryRun); err != nil {
			return err
		}
		if !*dryRun {
			return store.MarkDelivered(ctx, "email", radar.DigestHash(content))
		}
		return nil
	case "run":
		return serve(app, config)
	default:
		return fmt.Errorf("unknown command %q (use run, sync, digest, or check-config)", command)
	}
}

func serve(app *radar.Radar, config radar.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.Run(ctx)
	server := &http.Server{Addr: config.ListenAddress, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.InfoContext(ctx, "listening", "app", config.AppName, "address", config.ListenAddress)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		// Wait for in-flight requests to drain so the deferred trace flush
		// does not race the drain and drop their spans.
		<-drained
		return nil
	}
	return err
}

func setLogger(config radar.Config) error {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(config.LogLevel)); err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if config.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, options)
	} else {
		handler = slog.NewTextHandler(os.Stdout, options)
	}
	slog.SetDefault(slog.New(observ.NewTraceHandler(handler)))
	return nil
}
