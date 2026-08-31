package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func runBackupCommand(args []string) error {
	var dbPath, outPath string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			fmt.Println("Usage: onwatch backup --out FILE [--db PATH]")
			return nil
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !ok {
			if i+1 >= len(args) {
				return fmt.Errorf("backup: %s requires a value", arg)
			}
			i++
			value = args[i]
		}
		switch key {
		case "db":
			dbPath = value
		case "out":
			outPath = value
		default:
			return fmt.Errorf("backup: unknown option %q", arg)
		}
	}
	if outPath == "" {
		return fmt.Errorf("backup: --out is required")
	}
	if dbPath == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		dbPath = cfg.DBPath
	}
	database, err := store.New(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	metadata, err := database.Backup(outPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Backup created: %s (%d bytes, sha256 %s)\n", outPath, metadata.Size, metadata.SHA256)
	return nil
}
