package collector

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type Runtime struct {
	cfg            Config
	spool          *Spool
	client         *Client
	usage          *agentusage.Collector
	logger         *slog.Logger
	mu             sync.Mutex
	sourceOffsets  map[string]int64
	revision       int64
	desired        ingest.DesiredConfig
	authPaused     bool
	authPauseUntil time.Time
	retryUntil     time.Time
	retryAttempt   int
	quotaPolls     map[string]quotaPollState
	now            func() time.Time
	random         func() float64
}

func NewRuntime(cfg Config, logger *slog.Logger) (*Runtime, error) {
	spool, err := NewSpool(cfg.SpoolDir, cfg.SpoolMaxBytes)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	pricing, err := agentusage.LoadPricingMapFromEnv()
	if err != nil {
		pricing, _ = agentusage.DefaultPricingMap()
	}
	outputDir := filepath.Join(cfg.SpoolDir, "source-output")
	usage := agentusage.NewCollector(outputDir, pricing, agentusage.DefaultSources(cfg.HomeDir), logger)
	runtime := &Runtime{
		cfg: cfg, spool: spool, client: NewClient(cfg.ServerURL, cfg.DeviceID, cfg.Token), usage: usage,
		logger: logger, sourceOffsets: map[string]int64{}, quotaPolls: map[string]quotaPollState{},
		now: time.Now, random: rand.Float64,
	}
	runtime.loadLocalState()
	return runtime, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	collectTicker := time.NewTicker(r.cfg.CollectInterval)
	defer collectTicker.Stop()
	uploadTicker := time.NewTicker(r.cfg.UploadInterval)
	defer uploadTicker.Stop()
	heartbeatTicker := time.NewTicker(60 * time.Second)
	defer heartbeatTicker.Stop()
	_ = r.collectOnce(ctx)
	_ = r.uploadOnce(ctx)
	_ = r.heartbeatOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = r.uploadOnce(shutdown)
			_ = r.heartbeatOnce(shutdown)
			return nil
		case <-collectTicker.C:
			if err := r.collectOnce(ctx); err != nil {
				r.logger.Error("collector source cycle failed", "error", err)
			}
		case <-uploadTicker.C:
			if err := r.uploadOnce(ctx); err != nil {
				r.logger.Warn("collector upload delayed", "error", err)
			}
		case <-heartbeatTicker.C:
			if err := r.heartbeatOnce(ctx); err != nil {
				r.logger.Warn("collector heartbeat delayed", "error", err)
			}
		}
	}
}

func (r *Runtime) collectOnce(ctx context.Context) error {
	status, err := r.spool.Status()
	if err != nil {
		return err
	}
	if status.Full {
		return fmt.Errorf("spool hard cap reached")
	}
	usageErr := r.usage.CollectOnce()
	dir := filepath.Join(r.cfg.SpoolDir, "source-output")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if usageErr != nil {
			return usageErr
		}
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "agent-usage-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		offset := r.sourceOffsets[name]
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		reader := bufio.NewReader(file)
		for {
			line, err := reader.ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				file.Close()
				return err
			}
			nextOffset := offset + int64(len(line))
			event, err := r.usageEnvelope(line)
			if err != nil {
				file.Close()
				return err
			}
			if err := r.spool.Append(event); err != nil {
				file.Close()
				return err
			}
			offset = nextOffset
			r.sourceOffsets[name] = offset
			if err := r.saveLocalState(); err != nil {
				file.Close()
				return err
			}
		}
		_ = file.Close()
		if name != "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl" {
			if info, statErr := os.Stat(path); statErr == nil && offset >= info.Size() {
				if removeErr := os.Remove(path); removeErr == nil {
					delete(r.sourceOffsets, name)
					_ = r.saveLocalState()
				}
			}
		}
	}
	if r.authPaused {
		return nil
	}
	quotaErr := r.collectAssignedQuotas(ctx)
	if usageErr != nil {
		return usageErr
	}
	return quotaErr
}

func (r *Runtime) usageEnvelope(line []byte) (ingest.Event, error) {
	var wire map[string]any
	if err := json.Unmarshal(line, &wire); err != nil {
		return ingest.Event{}, err
	}
	metadata, _ := wire["metadata"].(map[string]any)
	delete(metadata, "source_path")
	clean, err := json.Marshal(wire)
	if err != nil {
		return ingest.Event{}, err
	}
	ts, err := time.Parse(time.RFC3339Nano, stringValue(wire["ts"]))
	if err != nil {
		return ingest.Event{}, err
	}
	provider := strings.ToLower(stringValue(wire["provider"]))
	account := stringValue(wire["account"])
	if account == "" {
		account = "default"
	}
	key := stringValue(metadata["event_key"])
	if key == "" {
		digest := sha256.Sum256(append([]byte(r.cfg.DeviceID), clean...))
		key = hex.EncodeToString(digest[:])
	}
	event := ingest.Event{EventID: "evt_" + key, Kind: "usage_event", CapturedAt: ts.UTC(), Provider: provider, Account: ingest.Account{ExternalID: account}, Payload: clean}
	if err := event.Validate(time.Now().UTC()); err != nil {
		return ingest.Event{}, err
	}
	return event, nil
}

func (r *Runtime) uploadOnce(ctx context.Context) error {
	if r.authPaused || time.Now().Before(r.retryUntil) {
		return nil
	}
	records, err := r.spool.Batch(r.cfg.BatchSize, ingest.MaxRequestBytes-4096)
	if err != nil || len(records) == 0 {
		return err
	}
	events := make([]ingest.Event, len(records))
	for i := range records {
		events[i] = records[i].Event
	}
	response, status, err := r.client.Upload(ctx, events, r.revision)
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			r.authPaused = true
			r.authPauseUntil = time.Now().Add(15 * time.Minute)
			r.retryAttempt = 0
		} else {
			delay := retryDelay(r.retryAttempt)
			r.retryAttempt++
			r.retryUntil = time.Now().Add(delay)
		}
		_ = r.spool.SetError(err.Error())
		_ = r.saveLocalState()
		return err
	}
	if len(response.Results) != len(records) {
		return fmt.Errorf("server acknowledgement count mismatch")
	}
	for i, result := range response.Results {
		if result.EventID != records[i].Event.EventID {
			return fmt.Errorf("server acknowledgement order mismatch")
		}
		if result.Status == "rejected" {
			_ = r.quarantine(records[i].Event, result)
		}
	}
	now := time.Now().UTC()
	if err := r.spool.Ack(records, now); err != nil {
		return err
	}
	if response.ConfigRevision != r.revision {
		if err := ingest.ValidateDesiredConfig(response.DesiredConfig); err != nil || response.DesiredConfig.Revision != response.ConfigRevision {
			return fmt.Errorf("server returned invalid desired configuration")
		}
		r.revision, r.desired = response.ConfigRevision, response.DesiredConfig
	}
	r.retryAttempt, r.retryUntil = 0, time.Time{}
	return r.saveLocalState()
}

func (r *Runtime) heartbeatOnce(ctx context.Context) error {
	if r.authPaused && time.Now().Before(r.authPauseUntil) {
		return nil
	}
	status, err := r.spool.Status()
	if err != nil {
		return err
	}
	heartbeat := ingest.Heartbeat{SchemaVersion: ingest.SchemaVersion, DeviceID: r.cfg.DeviceID, CollectorVersion: Version(), Platform: runtimePlatform(), ConfigRevision: r.revision, QueueBytes: status.QueueBytes, PendingEvents: status.PendingEvents, OldestQueuedAt: status.OldestQueuedAt, LastUploadAt: status.LastUploadAt, Capabilities: []string{"usage_event_v1", "quota_snapshot_v1"}}
	response, code, err := r.client.Heartbeat(ctx, heartbeat)
	if err != nil {
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			r.authPaused = true
			r.authPauseUntil = time.Now().Add(15 * time.Minute)
			_ = r.saveLocalState()
		}
		return err
	}
	r.authPaused, r.authPauseUntil = false, time.Time{}
	if response.ConfigRevision != r.revision {
		if err := ingest.ValidateDesiredConfig(response.DesiredConfig); err != nil || response.DesiredConfig.Revision != response.ConfigRevision {
			return fmt.Errorf("server returned invalid desired configuration")
		}
		r.revision, r.desired = response.ConfigRevision, response.DesiredConfig
	}
	return r.saveLocalState()
}

type localState struct {
	SourceOffsets  map[string]int64          `json:"source_offsets"`
	ConfigRevision int64                     `json:"config_revision"`
	DesiredConfig  ingest.DesiredConfig      `json:"desired_config"`
	QuotaPolls     map[string]quotaPollState `json:"quota_polls,omitempty"`
	AuthPauseUntil *time.Time                `json:"auth_pause_until,omitempty"`
	AuthPaused     bool                      `json:"auth_paused,omitempty"`
}

func (r *Runtime) loadLocalState() {
	data, err := os.ReadFile(filepath.Join(r.cfg.SpoolDir, "collector-state.json"))
	if err != nil {
		return
	}
	var state localState
	if json.Unmarshal(data, &state) == nil {
		if state.SourceOffsets != nil {
			r.sourceOffsets = state.SourceOffsets
		}
		if state.QuotaPolls != nil {
			r.quotaPolls = state.QuotaPolls
		}
		r.revision = state.ConfigRevision
		r.desired = state.DesiredConfig
		if state.AuthPaused {
			r.authPaused = true
			if state.AuthPauseUntil != nil {
				r.authPauseUntil = *state.AuthPauseUntil
			}
		}
	}
}
func (r *Runtime) saveLocalState() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var authPauseUntil *time.Time
	if r.authPaused && r.authPauseUntil.After(time.Now()) {
		pause := r.authPauseUntil
		authPauseUntil = &pause
	}
	state := localState{SourceOffsets: r.sourceOffsets, ConfigRevision: r.revision, DesiredConfig: r.desired, QuotaPolls: r.quotaPolls, AuthPauseUntil: authPauseUntil, AuthPaused: r.authPaused}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(r.cfg.SpoolDir, ".collector-state-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(r.cfg.SpoolDir, "collector-state.json")); err != nil {
		return err
	}
	dir, err := os.Open(r.cfg.SpoolDir)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func (r *Runtime) quarantine(event ingest.Event, result ingest.EventResult) error {
	path := filepath.Join(r.cfg.SpoolDir, "quarantine.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, _ := json.Marshal(struct {
		Event  ingest.Event       `json:"event"`
		Result ingest.EventResult `json:"result"`
	}{event, result})
	encoded = append(encoded, '\n')
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	return file.Sync()
}
func stringValue(value any) string { result, _ := value.(string); return strings.TrimSpace(result) }
func runtimePlatform() string      { return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH) }
