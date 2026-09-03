package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const launchdServiceLabel = "dev.onllm.onwatch"

func launchdPlistPath() string {
	dir := strings.TrimSpace(os.Getenv("ONWATCH_LAUNCH_AGENT_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return filepath.Join(dir, launchdServiceLabel+".plist")
}

func launchdServiceInstalled() bool {
	executable, _ := os.Executable()
	if runtime.GOOS != "darwin" || os.Getenv("ONWATCH_LAUNCHD") == "1" || strings.HasSuffix(filepath.Base(executable), ".test") {
		return false
	}
	_, err := os.Stat(launchdPlistPath())
	return err == nil
}

func launchdTarget() (string, error) {
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		return "", fmt.Errorf("get launchd user ID: %w", err)
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" {
		return "", fmt.Errorf("get launchd user ID: empty result")
	}
	return "gui/" + uid, nil
}

func launchdServiceLoaded() bool {
	if !launchdServiceInstalled() {
		return false
	}
	domain, err := launchdTarget()
	if err != nil {
		return false
	}
	return exec.Command("launchctl", "print", domain+"/"+launchdServiceLabel).Run() == nil
}

func startLaunchdService() error {
	domain, err := launchdTarget()
	if err != nil {
		return err
	}
	if !launchdServiceLoaded() {
		if err := exec.Command("launchctl", "bootstrap", domain, launchdPlistPath()).Run(); err != nil {
			return fmt.Errorf("start launchd service: %w", err)
		}
	}
	if err := exec.Command("launchctl", "kickstart", "-k", domain+"/"+launchdServiceLabel).Run(); err != nil {
		return fmt.Errorf("restart launchd service: %w", err)
	}
	fmt.Println("onWatch started under launchd")
	return nil
}

func stopLaunchdService() (bool, error) {
	if !launchdServiceLoaded() {
		return false, nil
	}
	domain, err := launchdTarget()
	if err != nil {
		return true, err
	}
	if err := exec.Command("launchctl", "bootout", domain, launchdPlistPath()).Run(); err != nil {
		return true, fmt.Errorf("stop launchd service: %w", err)
	}
	_ = os.Remove(pidFile)
	fmt.Println("Stopped onwatch launchd service")
	return true, nil
}
