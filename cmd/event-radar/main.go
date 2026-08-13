package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lucabecker/event-radar/internal/radar"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
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
	store, err := radar.OpenStore(config.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
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
			log.Printf("sync warning: %v", err)
		}
		events, err := app.Events(ctx)
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
		if err := radar.SendDigest(config, content, *dryRun); err != nil {
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
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("%s listening on http://%s", config.AppName, config.ListenAddress)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
