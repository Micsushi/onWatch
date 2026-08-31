package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func runDeviceCommand(args []string) error {
	action, options, err := parseDeviceArgs(args)
	if err != nil {
		return err
	}
	if action == "help" || action == "" {
		printDeviceHelp()
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	dbPath := cfg.DBPath
	if options["db"] != "" {
		dbPath = options["db"]
	}
	if err := validateDeviceOptions(action, options); err != nil {
		return err
	}
	database, err := store.New(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	switch action {
	case "create":
		device, token, err := database.CreateDevice(options["name"], options["platform"])
		if err != nil {
			return err
		}
		fmt.Printf("Device ID: %s\nToken: %s\n", device.ID, token)
		fmt.Println("Store the token now. It will not be shown again.")
	case "list":
		devices, err := database.ListDevices()
		if err != nil {
			return err
		}
		if options["json"] == "true" {
			return json.NewEncoder(os.Stdout).Encode(deviceListOutput(devices))
		}
		for _, device := range devices {
			fmt.Printf("%s\t%s\t%s\t%s\n", device.ID, device.Name, device.Platform, deviceState(device, time.Now().UTC()))
		}
	case "rotate":
		token, err := database.RotateDeviceToken(options["device-id"])
		if err != nil {
			return err
		}
		fmt.Printf("Token: %s\nStore the replacement now. The previous token is invalid.\n", token)
	case "revoke":
		if err := database.RevokeDevice(options["device-id"]); err != nil {
			return err
		}
		fmt.Printf("Revoked %s\n", options["device-id"])
	case "assign":
		ownerKind := options["owner"]
		deviceID := options["device-id"]
		if ownerKind == "" {
			ownerKind = "device"
		}
		provider := strings.ToLower(options["provider"])
		if provider == "codex" {
			provider = "openai"
		}
		if ownerKind == "device" {
			device, err := database.GetDevice(deviceID)
			if err != nil {
				return err
			}
			desired := device.DesiredConfig
			interval := options["poll-interval"]
			if interval == "" {
				interval = "60s"
			}
			assignment := ingest.ProviderAssignment{Provider: provider, ExternalID: options["account"], CredentialAlias: options["credential-alias"], PollInterval: interval}
			replaced := false
			for i := range desired.Assignments {
				if desired.Assignments[i].Provider == assignment.Provider && desired.Assignments[i].ExternalID == assignment.ExternalID {
					desired.Assignments[i] = assignment
					replaced = true
				}
			}
			if !replaced {
				desired.Assignments = append(desired.Assignments, assignment)
			}
			if err := ingest.ValidateDesiredConfig(desired); err != nil {
				return err
			}
			if err := database.SetPollOwner(provider, options["account"], ownerKind, deviceID); err != nil {
				return err
			}
			if err := database.SetDeviceDesiredConfig(deviceID, desired); err != nil {
				_ = database.ClearPollOwner(provider, options["account"])
				return err
			}
		} else if err := database.SetPollOwner(provider, options["account"], ownerKind, ""); err != nil {
			return err
		}
		fmt.Println("Poll owner assigned")
	case "unassign":
		if err := database.ClearPollOwner(options["provider"], options["account"]); err != nil {
			return err
		}
		fmt.Println("Poll owner cleared; history was preserved")
	case "owners":
		owners, err := database.ListPollOwners()
		if err != nil {
			return err
		}
		if options["json"] == "true" {
			return json.NewEncoder(os.Stdout).Encode(owners)
		}
		for _, owner := range owners {
			fmt.Printf("%s\t%s\t%s\t%s\n", owner.Provider, owner.ExternalID, owner.OwnerKind, owner.DeviceID)
		}
	default:
		return fmt.Errorf("unknown device action %q", action)
	}
	return nil
}

func parseDeviceArgs(args []string) (string, map[string]string, error) {
	options := map[string]string{}
	action := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "device" {
			continue
		}
		if arg == "--help" || arg == "-h" {
			return "help", options, nil
		}
		if !strings.HasPrefix(arg, "-") && action == "" {
			action = arg
			continue
		}
		if arg == "--json" {
			options["json"] = "true"
			continue
		}
		if strings.HasPrefix(arg, "--") {
			key := strings.TrimPrefix(arg, "--")
			if cut := strings.IndexByte(key, '='); cut >= 0 {
				options[key[:cut]] = key[cut+1:]
				continue
			}
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires a value", arg)
			}
			options[key] = args[i+1]
			i++
			continue
		}
	}
	return action, options, nil
}

func validateDeviceOptions(action string, options map[string]string) error {
	required := map[string][]string{
		"create":   {"name"},
		"rotate":   {"device-id"},
		"revoke":   {"device-id"},
		"assign":   {"provider", "account"},
		"unassign": {"provider", "account"},
	}
	for _, key := range required[action] {
		if strings.TrimSpace(options[key]) == "" {
			return fmt.Errorf("device %s requires --%s", action, key)
		}
	}
	if action == "assign" {
		owner := options["owner"]
		if owner != "" && owner != "device" && owner != "server" {
			return fmt.Errorf("device assign --owner must be server or device")
		}
		if owner != "server" && strings.TrimSpace(options["device-id"]) == "" {
			return fmt.Errorf("device assign requires --device-id for a device owner")
		}
	}
	return nil
}

type deviceOutput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	Version       string `json:"version"`
	State         string `json:"state"`
	QueueBytes    int64  `json:"queue_bytes"`
	PendingEvents int    `json:"pending_events"`
}

func deviceListOutput(devices []store.Device) []deviceOutput {
	output := make([]deviceOutput, 0, len(devices))
	now := time.Now().UTC()
	for _, d := range devices {
		output = append(output, deviceOutput{d.ID, d.Name, d.Platform, d.CollectorVersion, deviceState(d, now), d.QueueBytes, d.PendingEvents})
	}
	return output
}
func deviceState(device store.Device, now time.Time) string {
	if device.RevokedAt != nil {
		return "revoked"
	}
	if device.LastHeartbeatAt == nil {
		return "never"
	}
	age := now.Sub(*device.LastHeartbeatAt)
	if age <= 3*time.Minute {
		return "current"
	}
	if age <= 15*time.Minute {
		return "delayed"
	}
	return "stale"
}

func printDeviceHelp() {
	fmt.Println("onWatch device management")
	fmt.Println("Usage: onwatch device create --name NAME [--platform OS]")
	fmt.Println("       onwatch device list [--json]")
	fmt.Println("       onwatch device rotate|revoke --device-id ID")
	fmt.Println("       onwatch device assign --provider NAME --account ID --owner server|device [--device-id ID] [--credential-alias NAME] [--poll-interval 60s]")
	fmt.Println("       onwatch device unassign --provider NAME --account ID")
	fmt.Println("       onwatch device owners [--json]")
	fmt.Println("All commands accept --db PATH.")
}
