package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigureAntigravityBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binding # test.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	device, _, err := s.CreateDevice("synthetic", "windows")
	if err != nil {
		t.Fatal(err)
	}
	original := `{"revision":5,"future":{"integer":9007199254740993},"sources":["codex"],"assignments":[{"provider":"antigravity","external_id":"opaque-one","credential_alias":"unchanged","poll_interval":"60s","future":{"keep":true}},{"provider":"antigravity","external_id":"opaque-two","antigravity_account_email":"other@example.invalid","poll_interval":"60s"}]}`
	seed := func(raw string) {
		t.Helper()
		_, err := s.db.Exec(`UPDATE devices SET desired_config_revision=5, desired_config_json=?, revoked_at=NULL WHERE device_id=?`, raw, device.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func() (int64, string) {
		t.Helper()
		var rev int64
		var raw string
		if err := s.db.QueryRow(`SELECT desired_config_revision, desired_config_json FROM devices WHERE device_id=?`, device.ID).Scan(&rev, &raw); err != nil {
			t.Fatal(err)
		}
		return rev, raw
	}
	seed(original)
	if next, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "Owner@example.invalid", 5, false); err != nil || next != 6 {
		t.Fatalf("preview: next=%d err=%v", next, err)
	}
	if rev, raw := read(); rev != 5 || raw != original {
		t.Fatal("preview changed configuration")
	}
	for _, tc := range []struct {
		name, account, email string
		revision             int64
	}{
		{"stale", "opaque-one", "owner@example.invalid", 4},
		{"missing", "missing", "owner@example.invalid", 5},
		{"malformed", "opaque-one", "invalid", 5},
		{"control", "opaque-one", "owner@example.invalid\n", 5},
		{"duplicate", "opaque-one", "other@example.invalid", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ConfigureAntigravityBinding(path, device.ID, tc.account, tc.email, tc.revision, true); err == nil {
				t.Fatal("unsafe update accepted")
			}
			if rev, raw := read(); rev != 5 || raw != original {
				t.Fatal("rejected operation changed configuration")
			}
		})
	}
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "Owner@example.invalid", 5, true); err != nil {
		t.Fatal(err)
	}
	rev, raw := read()
	if rev != 6 || !strings.Contains(raw, `9007199254740993`) || !strings.Contains(raw, `"antigravity_account_email":"Owner@example.invalid"`) {
		t.Fatalf("binding/number/revision lost: %d %s", rev, raw)
	}
	// Removing only the new field and revision must recover every other field.
	var restored string
	if err := s.db.QueryRow(`SELECT json_set(json_remove(?, '$.assignments[0].antigravity_account_email'), '$.revision', 5)`, raw).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != original {
		t.Fatal("unrelated configuration changed")
	}
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "", 6, true); err != nil {
		t.Fatal(err)
	}
	_, raw = read()
	if strings.Contains(raw, "Owner@example.invalid") {
		t.Fatal("clear did not restore absent binding")
	}
	// Roll back a replacement with a fresh revision, preserving the existing email exactly.
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-two", "new@example.invalid", 7, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-two", "other@example.invalid", 8, true); err != nil {
		t.Fatal(err)
	}
	_, raw = read()
	if !strings.Contains(raw, "other@example.invalid") || strings.Contains(raw, "new@example.invalid") {
		t.Fatal("rollback failed")
	}
	seed(strings.Replace(original, `"revision":5`, `"revision":4,"revision":5`, 1))
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "owner@example.invalid", 5, true); err == nil {
		t.Fatal("ambiguous JSON accepted")
	}
	seed(strings.Replace(original, `"assignments"`, `"Assignments"`, 1))
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "owner@example.invalid", 5, true); err == nil {
		t.Fatal("noncanonical assignment keys accepted")
	}
	seed(original)
	if _, err := s.db.Exec(`UPDATE devices SET revoked_at='2026-01-01' WHERE device_id=?`, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", "owner@example.invalid", 5, true); err == nil {
		t.Fatal("revoked device accepted")
	}
	seed(original)
	// Two independent operators using the same preview cannot both overwrite it.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, email := range []string{"first@example.invalid", "second@example.invalid"} {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			_, err := ConfigureAntigravityBinding(path, device.ID, "opaque-one", email, 5, true)
			results <- err
		}(email)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if rev, _ := read(); successes != 1 || rev != 6 {
		t.Fatalf("concurrent overwrite: successes=%d revision=%d", successes, rev)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if _, err := ConfigureAntigravityBinding(missing, device.ID, "opaque-one", "owner@example.invalid", 5, true); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("operation created a database")
	}
}
