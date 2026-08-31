package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func runHealthcheckCommand(args []string) error {
	url := "http://127.0.0.1:9211/healthz"
	for i := 1; i < len(args); i++ {
		if args[i] == "--url" && i+1 < len(args) {
			i++
			url = args[i]
			continue
		}
		if strings.HasPrefix(args[i], "--url=") {
			url = strings.TrimPrefix(args[i], "--url=")
			continue
		}
		return fmt.Errorf("healthcheck: unknown argument %q", args[i])
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned %s", response.Status)
	}
	fmt.Fprintln(os.Stdout, "ok")
	return nil
}
