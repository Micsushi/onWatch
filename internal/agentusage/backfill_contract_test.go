package agentusage

import (
	"database/sql"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBackfillUsesNormalizedCacheCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE api_integration_usage_events (
		id INTEGER PRIMARY KEY, captured_at TEXT, integration_name TEXT, provider TEXT,
		model TEXT, metadata_json TEXT, speed_mode TEXT, input_tokens INTEGER,
		cached_input_tokens INTEGER, cache_creation_input_tokens INTEGER,
		output_tokens INTEGER, reasoning_output_tokens INTEGER, total_tokens INTEGER, cost_usd REAL);
		INSERT INTO api_integration_usage_events VALUES
		(1,'2026-09-07T12:00:00Z','Codex CLI','openai','gpt-6-astra','{}','fast',500,500,0,100,0,1100,NULL),
		(2,'2026-09-07T12:00:00Z','Codex CLI','openai','gpt-6-astra','{}','standard',500,500,0,100,0,1100,7),
		(3,'2026-09-07T12:00:00Z','Codex CLI','openai','unknown','{}','standard',500,500,0,100,0,1100,NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	run := func(model string, extra ...string) {
		t.Helper()
		args := append([]string{"run", "../../scripts/backfill_costs.go", "--db", path, "--model", model}, extra...)
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("backfill: %v\n%s", err, out)
		}
	}
	run("gpt-6-astra", "--dry-run")
	var cost sql.NullFloat64
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=1").Scan(&cost); err != nil || cost.Valid {
		t.Fatalf("dry run wrote cost: %v %v", cost, err)
	}
	run("gpt-6-astra", "--apply")
	run("gpt-6-astra", "--apply")
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=1").Scan(&cost); err != nil || !cost.Valid || math.Abs(cost.Float64-0.021) > 1e-9 {
		t.Fatalf("normalized cost: %v %v", cost, err)
	}
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=2").Scan(&cost); err != nil || cost.Float64 != 7 {
		t.Fatalf("historical cost changed: %v %v", cost, err)
	}
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=3").Scan(&cost); err != nil || cost.Valid {
		t.Fatalf("unknown model changed: %v %v", cost, err)
	}

	_, err = db.Exec(`ALTER TABLE api_integration_usage_events ADD COLUMN source_path TEXT NOT NULL DEFAULT '';
		INSERT INTO api_integration_usage_events VALUES
		(4,'2026-09-07T12:00:00Z','Codex CLI','openai','gpt-5.6-luna','{}','standard',500,500,0,100,0,1100,0.00115,'device:fixture'),
		(5,'2026-07-29T12:00:00Z','Codex CLI','openai','gpt-5.6-luna','{}','standard',500,500,0,100,0,1100,0.00115,'device:fixture'),
		(6,'2026-09-07T12:00:00Z','Codex CLI','openai','gpt-5.6-luna','{}','standard',500,500,0,100,0,1100,0.00115,'user-cost.jsonl')`)
	if err != nil {
		t.Fatal(err)
	}
	run("gpt-5.6-luna", "--correct-legacy", "--dry-run")
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=4").Scan(&cost); err != nil || cost.Float64 != 0.00115 {
		t.Fatalf("legacy dry run changed cost: %v %v", cost, err)
	}
	run("gpt-5.6-luna", "--correct-legacy", "--apply")
	run("gpt-5.6-luna", "--correct-legacy", "--apply")
	for id, want := range map[int]float64{4: 0.00023, 5: 0.00115, 6: 0.00115} {
		if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=?", id).Scan(&cost); err != nil || !cost.Valid || math.Abs(cost.Float64-want) > 1e-9 {
			t.Fatalf("row %d cost: got %v, want %v (%v)", id, cost, want, err)
		}
	}
}
