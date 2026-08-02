package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"zipmanga/internal/app"
	"zipmanga/internal/buildinfo"
	"zipmanga/internal/config"
	"zipmanga/internal/db"
)

func main() {
	logger := log.New(os.Stdout, "[zipmanga] ", log.LstdFlags|log.Lmicroseconds)

	args := os.Args[1:]
	if len(args) > 0 && args[0] == "db" {
		if err := runDBCommand(args[1:], logger); err != nil {
			logger.Fatalf("database command failed: %v", err)
		}
		return
	}
	if len(args) > 0 && args[0] == "version" {
		printVersion()
		return
	}

	if err := runServeCommand(args, logger); err != nil {
		logger.Fatalf("application error: %v", err)
	}
}

func runServeCommand(args []string, logger *log.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "./config.json", "path to config json")
	webDir := fs.String("web-dir", "./web", "path to web assets")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if logger != nil {
		info := buildinfo.Current()
		logger.Printf(
			"build info: version=%s commit=%s build_time=%s source_input_sha=%s web_asset_sha=%s",
			info.Version,
			info.Commit,
			info.BuildTime,
			info.SourceInputSHA,
			info.WebAssetSHA,
		)
	}

	application, err := app.New(*configPath, *webDir, logger)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return application.Run(ctx)
}

func runDBCommand(args []string, logger *log.Logger) error {
	if len(args) == 0 {
		return fmt.Errorf("missing database command, expected: db repair")
	}

	switch args[0] {
	case "repair":
		fs := flag.NewFlagSet("db repair", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		configPath := fs.String("config", "./config.json", "path to config json")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if err := repairDatabase(*configPath, logger); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown database command %q, expected: repair", args[0])
	}
}

func repairDatabase(configPath string, logger *log.Logger) error {
	created, err := config.EnsureDefaultFile(configPath)
	if err != nil {
		return err
	}
	if created && logger != nil {
		logger.Printf("config file not found, generated defaults: %s", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	store, err := db.Open(cfg.Database.Path, logger)
	if err != nil {
		return err
	}
	defer func() {
		_ = store.Close()
	}()

	if logger != nil {
		logger.Printf("database repair begin")
	}
	if err := store.ValidateAndRepairSchema(); err != nil {
		return err
	}
	if logger != nil {
		logger.Printf("database repair complete")
	}
	return nil
}

func printVersion() {
	info := buildinfo.Current()
	fmt.Printf("version=%s\n", info.Version)
	fmt.Printf("commit=%s\n", info.Commit)
	fmt.Printf("build_time=%s\n", info.BuildTime)
	fmt.Printf("source_input_sha=%s\n", info.SourceInputSHA)
	fmt.Printf("web_asset_sha=%s\n", info.WebAssetSHA)
}
