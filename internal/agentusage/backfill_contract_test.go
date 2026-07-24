package agentusage

import (
	"os"
	"strings"
	"testing"
)

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
