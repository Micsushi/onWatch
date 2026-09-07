//go:build ignore

// Run against an explicit onWatch DB and Codex rollout directory. Reads counters only.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/subscription"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	db := flag.String("db", "", "explicit onWatch database path")
	root := flag.String("source", "", "Codex sessions directory")
	account := flag.String("account", "default", "onWatch usage account")
	since := flag.String("since", "", "optional RFC3339 start")
	flag.Parse()
	if *db == "" || *root == "" {
		log.Fatal("--db and --source are required")
	}
	var start time.Time
	var err error
	if *since != "" {
		start, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			log.Fatal(err)
		}
	}
	s, err := store.New(*db)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	processed := 0
	err = filepath.WalkDir(*root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for scan.Scan() {
			line := scan.Bytes()
			if !strings.Contains(string(line), `"rate_limits"`) {
				continue
			}
			var row struct {
				At      time.Time `json:"timestamp"`
				Payload struct {
					Kind   string `json:"type"`
					Limits struct {
						ID        string  `json:"limit_id"`
						Plan      string  `json:"plan_type"`
						Primary   *window `json:"primary"`
						Secondary *window `json:"secondary"`
					} `json:"rate_limits"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &row) != nil || row.Payload.Kind != "token_count" || row.Payload.Limits.ID != "codex" || row.At.Before(start) {
				continue
			}
			q := row.Payload.Limits
			for _, w := range []*window{q.Primary, q.Secondary} {
				if w == nil || w.Used == nil || w.Reset <= 0 {
					continue
				}
				name := ""
				if w.Minutes == 10080 {
					name = "seven_day"
				} else if w.Minutes == 300 {
					name = "five_hour"
				}
				if name == "" {
					continue
				}
				if err := s.InsertSubscriptionMeter("codex", *account, subscription.Meter{At: row.At, Reset: time.Unix(w.Reset, 0).UTC(), Used: *w.Used, Quota: name, Plan: q.Plan}); err != nil {
					return err
				}
				processed++
			}
		}
		return scan.Err()
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Processed %d quota observations; replay uses unique observation keys. No token costs or transcripts were changed.\n", processed)
}

type window struct {
	Used    *float64 `json:"used_percent"`
	Minutes int      `json:"window_minutes"`
	Reset   int64    `json:"resets_at"`
}
