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
		SELECT s.captured_at, qv.quota_name, qv.utilization, qv.resets_at
		FROM anthropic_quota_values qv
		JOIN anthropic_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name IN ('five_hour','seven_day','seven_day_sonnet')
		  AND s.captured_at >= datetime('now','-9 days')
		ORDER BY s.captured_at ASC
	`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cap, name string
		var resets sql.NullString
		var util float64
		if err := rows.Scan(&cap, &name, &util, &resets); err != nil {
			panic(err)
		}
		fmt.Printf("%s  %-18s util=%7.4f%%  resets=%s\n", cap, name, util*100, resets.String)
	}
}
