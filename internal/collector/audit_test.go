package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPoisonSourceRecordPreservedWithoutBlockingFreshUsage(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			dir := t.TempDir()
			r, err := NewRuntime(Config{SpoolDir: dir, SpoolMaxBytes: 1 << 20, DeviceID: "dev_test00", HomeDir: t.TempDir()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "source-output")
			r.usage = agentusage.NewCollector(out, nil, nil, nil)
			if err := os.MkdirAll(out, 0700); err != nil {
				t.Fatal(err)
			}
			name := "agent-usage-" + time.Now().UTC().Format("2006-01-02") + ".jsonl"
			old := []byte(fmt.Sprintf("{\"ts\":%q,\"provider\":\"openai\",\"model\":\"test\"}\n", time.Now().Add(-365*24*time.Hour).UTC().Format(time.RFC3339Nano)))
			fresh := []byte(fmt.Sprintf("{\"ts\":%q,\"provider\":\"openai\",\"model\":\"test\"}\n", time.Now().UTC().Format(time.RFC3339Nano)))
			if err := os.WriteFile(filepath.Join(out, name), append(append([]byte{}, old...), fresh...), 0600); err != nil {
				t.Fatal(err)
			}
			if blocked {
				if err := os.Mkdir(filepath.Join(dir, "source-quarantine.jsonl"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err = r.collectOnce(context.Background())
			if blocked {
				if err == nil || r.sourceOffsets[name] != 0 {
					t.Fatalf("failed preservation advanced source: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			records, err := r.spool.Batch(10, 1<<20)
			if err != nil || len(records) != 1 {
				t.Fatalf("fresh usage blocked: %d %v", len(records), err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "source-quarantine.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Raw    []byte
				Reason string
			}
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(saved.Raw, old) || saved.Reason != "observation_too_old" {
				t.Fatal("original history was not retained exactly")
			}
			if err := r.collectOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(filepath.Join(dir, "source-quarantine.jsonl"))
			if !bytes.Equal(data, again) {
				t.Fatal("replay duplicated quarantine")
			}
		})
	}
}

func auditEvent() ingest.Event {
	return ingest.Event{EventID: "evt_audit00", Kind: "quota_snapshot", CapturedAt: time.Now().UTC(), Provider: "openai", Account: ingest.Account{ExternalID: "a"}, Payload: json.RawMessage(`{"version":1,"metrics":[{"name":"weekly","value":1,"unit":"percent"}]}`)}
}

func TestSourceDrainYieldsAndResumes(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRuntime(Config{SpoolDir: dir, SpoolMaxBytes: 1 << 20, DeviceID: "dev_test00", HomeDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "source-output")
	r.usage = agentusage.NewCollector(out, nil, nil, nil)
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	name := "agent-usage-" + time.Now().UTC().Format("2006-01-02") + ".jsonl"
	line := []byte(fmt.Sprintf("{\"ts\":%q,\"provider\":\"openai\",\"model\":\"test\"}\n", time.Now().UTC().Format(time.RFC3339Nano)))
	if err := os.WriteFile(filepath.Join(out, name), bytes.Repeat(line, 502), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.collectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.sourceOffsets[name] != int64(500*len(line)) {
		t.Fatal("archive drain did not yield at its budget")
	}
	if err := r.collectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.sourceOffsets[name] != int64(502*len(line)) {
		t.Fatal("archive drain did not resume after its saved cursor")
	}
}

func TestAuditUploadPreservesUnacknowledgedEvents(t *testing.T) {
	for _, status := range []string{"unexpected", "rejected"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			spool, err := NewSpool(dir, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err = spool.Append(auditEvent()); err != nil {
				t.Fatal(err)
			}
			if status == "rejected" {
				if err = os.Mkdir(filepath.Join(dir, "quarantine.jsonl"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"results":[{"event_id":"evt_audit00","status":%q}]}`, status)
			}))
			defer srv.Close()
			r := &Runtime{cfg: Config{SpoolDir: dir, BatchSize: 10}, spool: spool, client: NewClient(srv.URL, "device", "token")}
			if err = r.uploadOnce(context.Background()); err == nil {
				t.Fatal("unsafe acknowledgement accepted")
			}
			records, err := spool.Batch(10, 1<<20)
			if err != nil || len(records) != 1 {
				t.Fatalf("event lost: %d %v", len(records), err)
			}
		})
	}
}

func TestAuditSpoolPartialTailDoesNotPoisonNextAppend(t *testing.T) {
	dir := t.TempDir()
	spool, _ := NewSpool(dir, 1<<20)
	path := filepath.Join(dir, "events-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, []byte(`{"event_id":"partial"`), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	spool, err = NewSpool(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = spool.recoverPartialTails(); err != nil {
		t.Fatal(err)
	}
	if err := spool.Append(auditEvent()); err == nil {
		records, err := spool.Batch(10, 1<<20)
		if err != nil || len(records) != 1 {
			t.Fatalf("successful append stranded event: %d %v", len(records), err)
		}
	}
}

func TestAuditCollectorSpoolHasOneWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.lock")
	one, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	if err = lockCollectorFile(one); err != nil {
		t.Fatal(err)
	}
	if lockCollectorFile(two) == nil {
		t.Fatal("second writer acquired same spool")
	}
	one.Close()
	if err = lockCollectorFile(two); err != nil {
		t.Fatalf("lock not released on close: %v", err)
	}
}
