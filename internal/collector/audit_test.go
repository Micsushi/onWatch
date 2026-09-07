package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func auditEvent() ingest.Event {
	return ingest.Event{EventID: "evt_audit00", Kind: "quota_snapshot", CapturedAt: time.Now().UTC(), Provider: "openai", Account: ingest.Account{ExternalID: "a"}, Payload: json.RawMessage(`{"version":1,"metrics":[{"name":"weekly","value":1,"unit":"percent"}]}`)}
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
