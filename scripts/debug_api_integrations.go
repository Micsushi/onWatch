//go:build ignore

package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", os.Getenv("USERPROFILE")+`\.onwatch\data\onwatch.db`)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT model, COUNT(*) AS reqs, SUM(total_tokens) AS toks,
		       SUM(COALESCE(cost_usd, 0)) AS cost,
		       SUM(CASE WHEN cost_usd IS NULL THEN 1 ELSE 0 END) AS null_cost
		FROM api_integration_usage_events
		WHERE captured_at >= datetime('now','-24 hours')
		GROUP BY model
		ORDER BY toks DESC
	`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	fmt.Printf("%-40s %8s %15s %12s %10s\n", "model", "reqs", "tokens", "cost", "null_cost")
	for rows.Next() {
		var model sql.NullString
		var reqs, toks, nullCost int64
		var cost float64
		if err := rows.Scan(&model, &reqs, &toks, &cost, &nullCost); err != nil {
			panic(err)
		}
		fmt.Printf("%-40s %8d %15d %12.4f %10d\n", model.String, reqs, toks, cost, nullCost)
	}
}
