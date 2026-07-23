package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type dataCommandOptions struct {
	command string
	dbPath  string
	outPath string
	files   []string
	help    bool
}

func runDataCommand(args []string) error {
	opts, err := parseDataCommand(args)
	if err != nil {
		return err
	}
	if opts.help {
		printDataHelp()
		return nil
	}
	if opts.dbPath == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("data: load config: %w", err)
		}
		opts.dbPath = cfg.DBPath
	}

	s, err := store.New(opts.dbPath)
	if err != nil {
		return fmt.Errorf("data: open database: %w", err)
	}
	defer s.Close()
	if opts.command == "export" {
		return runDataExport(s, opts.outPath)
	}
	return runDataImport(s, opts.files)
}

func parseDataCommand(args []string) (dataCommandOptions, error) {
	var opts dataCommandOptions
	if len(args) < 2 || args[0] != "data" {
		return opts, fmt.Errorf("usage: onwatch data <export|import> [options]")
	}
	if args[1] == "--help" || args[1] == "-h" {
		opts.help = true
		return opts, nil
	}
	opts.command = args[1]
	if opts.command != "export" && opts.command != "import" {
		return opts, fmt.Errorf("data: unknown command %q", opts.command)
	}
	for index := 2; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			opts.help = true
		case arg == "--db":
			if index+1 >= len(args) {
				return opts, fmt.Errorf("data: --db requires a path")
			}
			index++
			opts.dbPath = args[index]
		case strings.HasPrefix(arg, "--db="):
			opts.dbPath = strings.TrimPrefix(arg, "--db=")
		case arg == "--out" && opts.command == "export":
			if index+1 >= len(args) {
				return opts, fmt.Errorf("data: --out requires a path")
			}
			index++
			opts.outPath = args[index]
		case strings.HasPrefix(arg, "--out=") && opts.command == "export":
			opts.outPath = strings.TrimPrefix(arg, "--out=")
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("data: unknown option %q", arg)
		case opts.command == "import":
			opts.files = append(opts.files, arg)
		default:
			return opts, fmt.Errorf("data export: unexpected argument %q", arg)
		}
	}
	if opts.help {
		return opts, nil
	}
	if opts.command == "import" && len(opts.files) == 0 {
		return opts, fmt.Errorf("data import: at least one .onwatch.zip file is required")
	}
	return opts, nil
}

func runDataExport(s *store.Store, outputPath string) error {
	if outputPath == "" {
		outputPath = "onwatch-history-" + time.Now().UTC().Format("20060102-150405") + ".onwatch.zip"
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("data export: %s already exists", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("data export: inspect output: %w", err)
	}

	temp, err := os.CreateTemp(filepath.Dir(outputPath), ".onwatch-export-*.tmp")
	if err != nil {
		return fmt.Errorf("data export: create temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	manifest, exportErr := s.ExportData(temp, store.ExportOptions{AppVersion: version})
	if syncErr := temp.Sync(); exportErr == nil && syncErr != nil {
		exportErr = syncErr
	}
	if closeErr := temp.Close(); exportErr == nil && closeErr != nil {
		exportErr = closeErr
	}
	if exportErr != nil {
		return fmt.Errorf("data export: %w", exportErr)
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("data export: %s already exists", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("data export: inspect output: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("data export: finalize archive: %w", err)
	}
	fmt.Printf("Exported %d records to %s\n", transferManifestRecordCount(manifest), outputPath)
	fmt.Println("Archive excludes credentials but may contain private usage and account data.")
	return nil
}

func runDataImport(s *store.Store, files []string) error {
	var total store.ImportTableSummary
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("data import %s: %w", path, err)
		}
		summary, importErr := s.ImportData(file)
		closeErr := file.Close()
		if importErr != nil {
			return fmt.Errorf("data import %s: %w", path, importErr)
		}
		if closeErr != nil {
			return fmt.Errorf("data import %s: close: %w", path, closeErr)
		}
		fmt.Printf("Imported %s: %d inserted, %d updated, %d skipped\n",
			path, summary.Total.Inserted, summary.Total.Updated, summary.Total.Skipped)
		total.Inserted += summary.Total.Inserted
		total.Updated += summary.Total.Updated
		total.Skipped += summary.Total.Skipped
	}
	if len(files) > 1 {
		fmt.Printf("Total: %d inserted, %d updated, %d skipped\n", total.Inserted, total.Updated, total.Skipped)
	}
	return nil
}

func transferManifestRecordCount(manifest store.TransferManifest) int {
	total := 0
	for _, count := range manifest.Counts {
		total += count
	}
	return total
}

func printDataHelp() {
	fmt.Print("onWatch Portable Data Transfer\n\n" +
		"Usage:\n" +
		"  onwatch data export [--db PATH] [--out FILE]\n" +
		"  onwatch data import [--db PATH] FILE...\n\n" +
		"Export creates a secret-safe .onwatch.zip archive. Import additively merges\n" +
		"records from every source device and skips records already imported.\n")
}
