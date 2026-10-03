// Package main provides the MCP server entry point for claude-mnemonic.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lukaszraczylo/claude-mnemonic/internal/config"
	"github.com/lukaszraczylo/claude-mnemonic/internal/mcp"
	"github.com/lukaszraczylo/claude-mnemonic/internal/watcher"
	"github.com/lukaszraczylo/claude-mnemonic/pkg/hooks"
	"github.com/lukaszraczylo/oss-telemetry"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Version is set at build time via ldflags.
var Version = "dev"

// defaultProject derives the project ID the same way the hooks do.
func defaultProject() string {
	dir := os.Getenv("CLAUDE_PROJECT_DIR")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return hooks.ProjectIDWithName(dir)
}

func main() {
	// Parse flags
	project := flag.String("project", "", "Project ID (default: derived from CLAUDE_PROJECT_DIR or cwd)")
	modeFlag := flag.String("mode", "auto", "Project mode: auto (detect from the MCP client), code (fixed project) or desktop (model chooses the project)")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	// Setup logging - MCP uses stdout for communication, so log to stderr
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, NoColor: true})

	mode, err := mcp.ParseMode(*modeFlag)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid --mode")
	}

	// An explicit --project is the user's choice and is kept even in Desktop mode.
	pinned := *project != "" && !strings.Contains(*project, "${")
	if !pinned {
		*project = defaultProject()
	}

	// Get worker port from config
	port := config.GetWorkerPort()
	workerURL := fmt.Sprintf("http://localhost:%d", port)

	// Create HTTP client for worker
	client := &http.Client{Timeout: 30 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("Shutting down MCP server")
		cancel()
	}()

	// Start file watchers for config changes
	startWatchers(cancel)

	telemetry.Send("claude-mnemonic", Version)

	// Create and run MCP server
	server := mcp.NewServer(client, workerURL, *project, Version)
	server.SetMode(mode)
	server.SetProjectPinned(pinned)
	// Desktop has no hooks to start the worker, so the server does it on the first tool call.
	server.SetWorkerBootstrap(func() error {
		_, err := hooks.EnsureWorkerRunning()
		return err
	})
	log.Info().Str("project", *project).Bool("pinned", pinned).Str("mode", string(mode)).
		Str("version", Version).Str("worker", workerURL).Msg("Starting MCP server")

	if err := server.Run(ctx); err != nil {
		if err == context.Canceled {
			log.Info().Msg("MCP server shut down (config change or signal)")
			return
		}
		log.Fatal().Err(err).Msg("MCP server error")
	}
}

// startWatchers initializes file watchers for config.
func startWatchers(cancel context.CancelFunc) {
	// Watch config file for changes (triggers graceful shutdown via context cancellation)
	configPath := config.SettingsPath()
	configWatcher, err := watcher.New(configPath, func() {
		log.Warn().Str("path", configPath).Msg("Config file changed, shutting down gracefully...")
		cancel() // Triggers ctx.Done() in server.Run(), which drains in-flight requests
	})
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create config watcher")
	} else {
		if err := configWatcher.Start(); err != nil {
			log.Warn().Err(err).Msg("Failed to start config watcher")
		} else {
			log.Info().Str("path", configPath).Msg("Config file watcher started")
		}
	}
}
