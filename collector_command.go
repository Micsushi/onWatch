package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/onllm-dev/onwatch/v2/internal/collector"
)

func runCollectorCommand(args []string) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			printCollectorHelp()
			return nil
		}
	}
	if collectorAction(args) == "status" {
		return printCollectorStatus(args)
	}
	if collectorAction(args) == "uninstall" {
		return uninstallCollectorService(args)
	}
	cfg, err := collector.LoadConfig(args)
	if err != nil {
		return err
	}
	if collectorAction(args) == "install" {
		return installCollectorService(cfg, false)
	}
	if collectorAction(args) == "restart" {
		return installCollectorService(cfg, true)
	}
	runtime, err := collector.NewRuntime(cfg, slog.Default())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("Collector started for %s\n", cfg.DeviceID)
	return runtime.Run(ctx)
}

func collectorAction(args []string) string {
	for _, arg := range args {
		if arg == "run" || arg == "status" || arg == "install" || arg == "uninstall" || arg == "restart" {
			return arg
		}
	}
	return "run"
}

func printCollectorStatus(args []string) error {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".onwatch", "collector-spool")
	maxBytes := int64(512 << 20)
	asJSON := false
	if env := strings.TrimSpace(os.Getenv("ONWATCH_COLLECTOR_SPOOL_DIR")); env != "" {
		dir = env
	}
	if env := strings.TrimSpace(os.Getenv("ONWATCH_COLLECTOR_SPOOL_MAX_BYTES")); env != "" {
		if value, err := strconv.ParseInt(env, 10, 64); err == nil {
			maxBytes = value
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--json" {
			asJSON = true
		}
		if arg == "--spool" && i+1 < len(args) {
			dir = args[i+1]
			i++
		}
		if strings.HasPrefix(arg, "--spool=") {
			dir = strings.TrimPrefix(arg, "--spool=")
		}
	}
	spool, err := collector.NewSpool(dir, maxBytes)
	if err != nil {
		return err
	}
	status, err := spool.Status()
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Queue: %d events, %d bytes\n", status.PendingEvents, status.QueueBytes)
	if status.OldestQueuedAt != nil {
		fmt.Printf("Oldest: %s\n", status.OldestQueuedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if status.LastUploadAt != nil {
		fmt.Printf("Last upload: %s\n", status.LastUploadAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	if status.LastError != "" {
		fmt.Printf("Last error: %s\n", status.LastError)
	}
	return nil
}

func printCollectorHelp() {
	fmt.Println("onWatch Collector")
	fmt.Println("Usage: onwatch collector [run] --server URL --device-id ID --token-file PATH [OPTIONS]")
	fmt.Println("       onwatch collector status [--spool PATH] [--json]")
	fmt.Println("       onwatch collector install|restart --server URL --device-id ID --token-file PATH [OPTIONS]")
	fmt.Println("       onwatch collector uninstall [--purge-spool]")
	fmt.Println("Options: --spool PATH --collect-interval 15s --upload-interval 15s --batch-size 500 --spool-max-bytes 536870912 --home PATH")
}
