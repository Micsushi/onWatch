package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func runDeviceBindingCommand(args []string) error {
	flags := flag.NewFlagSet("bind-antigravity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	db := flags.String("db", "", "existing database path")
	device := flags.String("device-id", "", "existing device ID")
	account := flags.String("account", "", "existing opaque account ID")
	email := flags.String("email", "", "intended account email")
	clear := flags.Bool("clear", false, "remove the binding")
	revision := flags.Int64("expected-revision", -1, "current configuration revision")
	apply := flags.Bool("apply", false, "apply the previewed change")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid bind-antigravity options; see device --help")
	}
	if flags.NArg() != 0 || *db == "" || *device == "" || *account == "" || *revision < 0 || (*clear == (strings.TrimSpace(*email) != "")) {
		return fmt.Errorf("bind-antigravity requires --db, --device-id, --account, --expected-revision and exactly one of --email or --clear")
	}
	next, err := store.ConfigureAntigravityBinding(*db, *device, *account, *email, *revision, *apply)
	if err != nil {
		return err
	}
	operation := "set"
	if *clear {
		operation = "remove"
	}
	if *apply {
		fmt.Printf("Binding %s applied at revision %d; live collection is not verified.\n", operation, next)
	} else {
		fmt.Printf("Preview: %s only the selected Antigravity email binding; revision %d -> %d. No configuration written. Re-run with --apply to save.\n", operation, *revision, next)
	}
	return nil
}
