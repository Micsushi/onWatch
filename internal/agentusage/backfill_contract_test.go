package agentusage

import (
	"database/sql"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	run := func(extra ...string) {
		t.Helper()
		args := append([]string{"run", "../../scripts/backfill_costs.go", "--db", path, "--model", "gpt-6-astra"}, extra...)
		if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
			t.Fatalf("backfill: %v\n%s", err, out)
		}
	}
	run("--dry-run")
	var cost sql.NullFloat64
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=1").Scan(&cost); err != nil || cost.Valid {
		t.Fatalf("dry run wrote cost: %v %v", cost, err)
	}
	run()
	run()
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=1").Scan(&cost); err != nil || !cost.Valid || math.Abs(cost.Float64-0.021) > 1e-9 {
		t.Fatalf("normalized cost: %v %v", cost, err)
	}
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=2").Scan(&cost); err != nil || cost.Float64 != 7 {
		t.Fatalf("historical cost changed: %v %v", cost, err)
	}
	if err := db.QueryRow("SELECT cost_usd FROM api_integration_usage_events WHERE id=3").Scan(&cost); err != nil || cost.Valid {
		t.Fatalf("unknown model changed: %v %v", cost, err)
	}
}

func TestBackfillOnlyFillsMissingHistoricalCosts(t *testing.T) {
	source, err := os.ReadFile("../../scripts/backfill_costs.go")
	if err != nil {
		t.Fatalf("read backfill script: %v", err)
	}
	text := string(source)
	if strings.Contains(text, "SET cost_usd = NULL") {
		t.Fatal("backfill must not clear stored costs before recalculating")
	}
	if !strings.Contains(text, "WHERE cost_usd IS NULL") {
		t.Fatal("backfill must select only rows with missing costs")
	}
	if !strings.Contains(text, "CalculateCostAt") {
		t.Fatal("backfill must price missing rows at their captured timestamp")
	}
}
