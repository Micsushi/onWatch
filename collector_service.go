package main

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/onllm-dev/onwatch/v2/internal/collector"
)

const collectorServiceLabel = "dev.onllm.onwatch.collector"

func installCollectorService(cfg collector.Config, restart bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	args := []string{"collector", "run", "--server", cfg.ServerURL, "--device-id", cfg.DeviceID, "--token-file", cfg.TokenFile, "--spool", cfg.SpoolDir, "--collect-interval", cfg.CollectInterval.String(), "--upload-interval", cfg.UploadInterval.String(), "--batch-size", fmt.Sprint(cfg.BatchSize), "--spool-max-bytes", fmt.Sprint(cfg.SpoolMaxBytes)}
	if cfg.HomeDir != "" {
		args = append(args, "--home", cfg.HomeDir)
	}
	switch runtime.GOOS {
	case "darwin":
		return installCollectorLaunchd(executable, args, restart)
	case "windows":
		return installCollectorScheduledTask(executable, args, cfg.TokenFile)
	default:
		return fmt.Errorf("collector service install is supported on macOS and Windows; run collector directly on %s", runtime.GOOS)
	}
}

func collectorLaunchdPath() string {
	dir := strings.TrimSpace(os.Getenv("ONWATCH_COLLECTOR_LAUNCH_AGENT_DIR"))
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return filepath.Join(dir, collectorServiceLabel+".plist")
}

func installCollectorLaunchd(executable string, args []string, restart bool) error {
	path := collectorLaunchdPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".onwatch", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return err
	}
	var items strings.Builder
	for _, arg := range append([]string{executable}, args...) {
		fmt.Fprintf(&items, "        <string>%s</string>\n", html.EscapeString(arg))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key><array>
%s    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>5</integer>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, collectorServiceLabel, items.String(), html.EscapeString(filepath.Join(logDir, "collector.log")), html.EscapeString(filepath.Join(logDir, "collector.log")))
	if err := os.WriteFile(path, []byte(plist), 0600); err != nil {
		return err
	}
	domain, err := collectorLaunchdTarget()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", domain, path).Run()
	if err := exec.Command("launchctl", "bootstrap", domain, path).Run(); err != nil {
		return fmt.Errorf("install collector launchd service: %w", err)
	}
	if restart {
		_ = exec.Command("launchctl", "kickstart", "-k", domain+"/"+collectorServiceLabel).Run()
	}
	fmt.Printf("Collector installed as %s\n", collectorServiceLabel)
	return nil
}

func installCollectorScheduledTask(executable string, args []string, tokenFile string) error {
	current, err := user.Current()
	if err != nil {
		return err
	}
	if err := exec.Command("icacls", tokenFile, "/inheritance:r", "/grant:r", current.Username+":(R)").Run(); err != nil {
		return fmt.Errorf("restrict collector token ACL: %w", err)
	}
	quoted := `"` + executable + `" ` + quoteWindowsArgs(args)
	command := exec.Command("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", "onWatch Collector", "/TR", quoted)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("create collector scheduled task: %s: %w", strings.TrimSpace(string(output)), err)
	}
	fmt.Println("Collector installed as Windows task onWatch Collector")
	return nil
}

func quoteWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
	}
	return strings.Join(quoted, " ")
}

func uninstallCollectorService(args []string) error {
	purge := false
	for _, arg := range args {
		if arg == "--purge-spool" {
			purge = true
		}
	}
	switch runtime.GOOS {
	case "darwin":
		path := collectorLaunchdPath()
		domain, _ := collectorLaunchdTarget()
		_ = exec.Command("launchctl", "bootout", domain, path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	case "windows":
		_ = exec.Command("schtasks", "/Delete", "/F", "/TN", "onWatch Collector").Run()
	default:
		return fmt.Errorf("collector service uninstall is supported on macOS and Windows")
	}
	if purge {
		home, _ := os.UserHomeDir()
		path := filepath.Join(home, ".onwatch", "collector-spool")
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	fmt.Println("Collector service removed; queued data was preserved unless --purge-spool was supplied")
	return nil
}

func collectorLaunchdTarget() (string, error) {
	output, err := exec.Command("id", "-u").Output()
	if err != nil {
		return "", err
	}
	uid := strings.TrimSpace(string(output))
	if uid == "" {
		return "", fmt.Errorf("empty launchd user ID")
	}
	return "gui/" + uid, nil
}
