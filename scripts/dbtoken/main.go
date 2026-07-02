// Debug helper: print a valid (unexpired) dashboard session token from the DB.
// Read-only. Used to verify the live dashboard during development.
//
//	go run ./scripts/dbtoken "C:/Users/sushi/.onwatch/data/onwatch.db"
package main

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	path := "C:/Users/sushi/.onwatch/data/onwatch.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := db.Query("SELECT token, expires_at FROM auth_tokens WHERE expires_at > ? ORDER BY expires_at DESC LIMIT 1", now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	defer rows.Close()
	for rows.Next() {
		var tok, exp string
		rows.Scan(&tok, &exp)
		fmt.Println(tok)
	}
}
