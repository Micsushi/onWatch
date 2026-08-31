package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type deviceHealthResponse struct {
	ID                  string             `json:"id"`
	Name                string             `json:"name"`
	Platform            string             `json:"platform"`
	CollectorVersion    string             `json:"collector_version"`
	State               string             `json:"state"`
	LastHeartbeatAt     *time.Time         `json:"last_heartbeat_at,omitempty"`
	LastAcceptedEventAt *time.Time         `json:"last_accepted_event_at,omitempty"`
	QueueBytes          int64              `json:"queue_bytes"`
	PendingEvents       int                `json:"pending_events"`
	OldestQueuedAt      *time.Time         `json:"oldest_queued_at,omitempty"`
	Assignments         []deviceAssignment `json:"assignments"`
}

type deviceAssignment struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
}

func (h *Handler) Devices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	devices, err := h.store.ListDevices()
	if err != nil {
		http.Error(w, "device status unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now().UTC()
	response := make([]deviceHealthResponse, 0, len(devices))
	for _, device := range devices {
		item := deviceHealthResponse{
			ID: device.ID, Name: device.Name, Platform: device.Platform, CollectorVersion: device.CollectorVersion,
			State: centralDeviceState(device, now), LastHeartbeatAt: device.LastHeartbeatAt,
			LastAcceptedEventAt: device.LastAcceptedEventAt, QueueBytes: device.QueueBytes,
			PendingEvents: device.PendingEvents, OldestQueuedAt: device.OldestQueuedAt,
			Assignments: make([]deviceAssignment, 0, len(device.DesiredConfig.Assignments)),
		}
		for _, assignment := range device.DesiredConfig.Assignments {
			item.Assignments = append(item.Assignments, deviceAssignment{Provider: assignment.Provider, ExternalID: assignment.ExternalID})
		}
		response = append(response, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"server_time": now, "devices": response})
}

func centralDeviceState(device store.Device, now time.Time) string {
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
