package ingestserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type Server struct {
	httpServer   *http.Server
	store        *store.Store
	logger       *slog.Logger
	metricsToken string
	accepted     atomic.Uint64
	duplicates   atomic.Uint64
	rejected     atomic.Uint64
	heartbeats   atomic.Uint64
}

func New(host string, port int, database *store.Store, logger *slog.Logger, metricsToken string) *Server {
	server := &Server{store: database, logger: logger, metricsToken: metricsToken}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/batches", server.batches)
	mux.HandleFunc("/v1/heartbeat", server.heartbeat)
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/metrics", server.metrics)
	server.httpServer = &http.Server{
		Addr: net.JoinHostPort(host, strconv.Itoa(port)), Handler: securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	return server
}

func (s *Server) Start() error                       { return s.httpServer.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.httpServer.Shutdown(ctx) }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.store == nil || s.store.Ready() != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, "ok\n")
	}
}

func (s *Server) batches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !contentTypeJSON(r.Header.Get("Content-Type")) {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ingest.MaxRequestBytes))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	now := time.Now().UTC()
	batch, err := ingest.DecodeBatch(body, now)
	if err != nil {
		http.Error(w, "invalid batch", http.StatusBadRequest)
		return
	}
	device, ok := s.authenticate(batch.DeviceID, r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	results := make([]ingest.EventResult, len(batch.Events))
	valid := make([]ingest.Event, 0, len(batch.Events))
	for i, event := range batch.Events {
		if err := event.Validate(now); err != nil {
			results[i] = ingest.EventResult{EventID: event.EventID, Status: "rejected", Code: stableCode(err)}
			s.rejected.Add(1)
			continue
		}
		valid = append(valid, event)
	}
	stored, err := s.store.StoreIngestBatch(device, valid, now)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("ingest batch storage failed", "device_id", device.ID, "events", len(valid), "error", err)
		}
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	storedByID := make(map[string]ingest.EventResult, len(stored))
	for _, result := range stored {
		storedByID[result.EventID] = result
		switch result.Status {
		case "accepted", "enriched":
			s.accepted.Add(1)
		case "duplicate":
			s.duplicates.Add(1)
		case "rejected":
			s.rejected.Add(1)
		}
	}
	for i, event := range batch.Events {
		if result, ok := storedByID[event.EventID]; ok {
			results[i] = result
		}
	}
	response := ingest.BatchResponse{ServerTime: now, ConfigRevision: device.DesiredConfigRevision, DesiredConfig: device.DesiredConfig, Results: results}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !contentTypeJSON(r.Header.Get("Content-Type")) {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var heartbeat ingest.Heartbeat
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&heartbeat)
	trailingErr := decoder.Decode(&struct{}{})
	if decodeErr != nil || trailingErr != io.EOF || heartbeat.SchemaVersion != ingest.SchemaVersion || ingest.ValidateDeviceID(heartbeat.DeviceID) != nil || heartbeat.QueueBytes < 0 || heartbeat.PendingEvents < 0 || len(heartbeat.CollectorVersion) > 128 || len(heartbeat.Platform) > 64 || len(heartbeat.Capabilities) > 64 {
		http.Error(w, "invalid heartbeat", http.StatusBadRequest)
		return
	}
	for _, capability := range heartbeat.Capabilities {
		if strings.TrimSpace(capability) == "" || len(capability) > 128 {
			http.Error(w, "invalid heartbeat", http.StatusBadRequest)
			return
		}
	}
	device, ok := s.authenticate(heartbeat.DeviceID, r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	collectorRevision := heartbeat.ConfigRevision
	device, err = s.store.RecordHeartbeat(device.ID, heartbeat)
	if err != nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	s.heartbeats.Add(1)
	response := ingest.HeartbeatResponse{ServerTime: time.Now().UTC(), ConfigRevision: device.DesiredConfigRevision}
	if collectorRevision != device.DesiredConfigRevision {
		response.DesiredConfig = device.DesiredConfig
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) authenticate(deviceID, authorization string) (*store.Device, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return nil, false
	}
	device, err := s.store.AuthenticateDevice(deviceID, strings.TrimSpace(strings.TrimPrefix(authorization, prefix)))
	return device, err == nil
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.metricsToken == "" || !bearerEqual(r.Header.Get("Authorization"), s.metricsToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "onwatch_ingest_events_total{status=\"accepted\"} %d\n", s.accepted.Load())
	fmt.Fprintf(w, "onwatch_ingest_events_total{status=\"duplicate\"} %d\n", s.duplicates.Load())
	fmt.Fprintf(w, "onwatch_ingest_events_total{status=\"rejected\"} %d\n", s.rejected.Load())
	fmt.Fprintf(w, "onwatch_ingest_heartbeats_total %d\n", s.heartbeats.Load())
	counts := map[string]int{"current": 0, "delayed": 0, "stale": 0, "revoked": 0, "never": 0}
	if devices, err := s.store.ListDevices(); err == nil {
		now := time.Now().UTC()
		for _, device := range devices {
			state := "never"
			if device.RevokedAt != nil {
				state = "revoked"
			} else if device.LastHeartbeatAt != nil {
				age := now.Sub(*device.LastHeartbeatAt)
				switch {
				case age <= 3*time.Minute:
					state = "current"
				case age <= 15*time.Minute:
					state = "delayed"
				default:
					state = "stale"
				}
			}
			counts[state]++
		}
	}
	for _, state := range []string{"current", "delayed", "stale", "revoked", "never"} {
		fmt.Fprintf(w, "onwatch_devices{state=%q} %d\n", state, counts[state])
	}
}

func bearerEqual(header, token string) bool {
	presented := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return len(presented) == len(token) && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func stableCode(err error) string {
	value := err.Error()
	if index := strings.LastIndex(value, ": "); index >= 0 {
		value = value[index+2:]
	}
	return strings.ReplaceAll(value, " ", "_")
}

func contentTypeJSON(value string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.Split(value, ";")[0]), "application/json")
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
