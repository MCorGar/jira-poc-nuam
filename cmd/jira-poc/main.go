package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"jira-poc-nuam/internal/poc"
)

func output(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}
func database(ctx context.Context) (*poc.Store, error) {
	store, err := poc.OpenStore(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, err
	}
	if err = store.Initialize(ctx); err != nil {
		store.Close()
		return nil, errors.New("no se pudo inicializar el esquema de PostgreSQL")
	}
	return store, nil
}
func run() error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	switch command {
	case "serve":
		provider, err := poc.NewProvider(env("DECISION_PROVIDER", "mock"), env("PROVIDER_FAILURE", "none"))
		if err != nil {
			return err
		}
		store, err := database(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		policy, err := poc.PolicyFromEnv()
		if err != nil {
			return err
		}
		requestTimeout, err := poc.ClassificationTimeoutFromEnv()
		if err != nil {
			return err
		}
		api := &poc.API{Store: store, Provider: provider, Token: os.Getenv("API_TOKEN"), Policy: policy, RequestTimeout: requestTimeout}
		server := &http.Server{Addr: env("LISTEN_ADDR", ":8000"), Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: requestTimeout + 5*time.Second, IdleTimeout: 60 * time.Second}
		signals, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		done := make(chan error, 1)
		go func() {
			log.Printf("API Go + GORM, proveedor %s, escuchando en %s", provider.Name(), server.Addr)
			done <- server.ListenAndServe()
		}()
		select {
		case err = <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-signals.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return server.Shutdown(shutdown)
		}
	case "import-catalog":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		file := flags.String("file", "", "Excel de origen")
		reportFile := flags.String("report", "", "Reporte JSON opcional")
		dryRun := flags.Bool("dry-run", false, "Generar catálogo sin acceder a PostgreSQL")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *file == "" {
			return errors.New("falta --file")
		}
		c, report, err := poc.ImportWorkbook(*file)
		if err != nil {
			return err
		}
		if *dryRun {
			return output(map[string]any{"catalog": c, "report": report})
		}
		store, err := database(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		if err = store.SaveCatalog(ctx, c, report); err != nil {
			return fmt.Errorf("importación no aplicada: %w", err)
		}
		if *reportFile != "" {
			if err = os.MkdirAll(filepath.Dir(*reportFile), 0700); err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(report, "", "  ")
			if err = os.WriteFile(*reportFile, raw, 0600); err != nil {
				return err
			}
		}
		return output(map[string]any{"catalog_version": c.Version, "counts": report["counts"], "issues": len(report["issues"].([]poc.Entity)), "provisional": true})
	case "verify-database":
		store, err := database(ctx)
		if err != nil {
			return err
		}
		defer store.Close()
		report, err := store.Verify(ctx)
		if err != nil {
			return err
		}
		return output(report)
	case "evaluate":
		return evaluate(os.Args[2:])
	case "prepare-local":
		return prepareLocal(os.Args[2:])
	case "postman-env":
		return postmanEnv(os.Args[2:])
	case "import-job":
		return importJob(os.Args[2:])
	case "generate-postman":
		return generatePostman(os.Args[2:])
	default:
		return fmt.Errorf("comando desconocido: %s", command)
	}
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
