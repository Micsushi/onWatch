package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditPasswordRotationRollsBackAllWrites(t *testing.T) {
	s := newTransferTestStore(t)
	if err := s.UpsertUser("admin", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("smtp", "old-ciphertext"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthToken("fixture", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_password BEFORE INSERT ON users BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.ChangePassword("admin", "old", "new", func(values map[string]string) error { values["smtp"] = "new-ciphertext"; return nil })
	if err == nil {
		t.Fatal("write failure ignored")
	}
	hash, _ := s.GetUser("admin")
	cipher, _ := s.GetSetting("smtp")
	_, found, _ := s.GetAuthTokenExpiry("fixture")
	if hash != "old" || cipher != "old-ciphertext" || !found {
		t.Fatal("partial password rotation committed")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_password"); err != nil {
		t.Fatal(err)
	}
	err = s.ChangePassword("admin", "old", "new", func(values map[string]string) error { return fmt.Errorf("bad ciphertext") })
	if err == nil {
		t.Fatal("transform failure ignored")
	}
}
func TestAuditExportReadsOneSourceSnapshot(t *testing.T) {
	s := newTransferTestStore(t)
	id, err := s.TransferInstallationID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.prepareTransferProvenance(id); err != nil {
		t.Fatal(err)
	}
	source, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Rollback()
	dest, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err := createTransferDatabase(dest); err != nil {
		t.Fatal(err)
	}
	tx, err := dest.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	counts := map[string]int{}
	if err := s.exportTransferAccounts(source, tx, id, false, counts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO anthropic_snapshots(captured_at,raw_json,quota_count) VALUES('2026-09-12T00:00:00Z','{}',1); INSERT INTO anthropic_quota_values(snapshot_id,quota_name,utilization) VALUES(last_insert_rowid(),'weekly',1)`); err != nil {
		t.Fatal(err)
	}
	for _, table := range transferTables {
		if table.name == "anthropic_snapshots" || table.name == "anthropic_quota_values" {
			if err := s.exportTransferTable(source, tx, table, id, false, counts); err != nil {
				t.Fatal(err)
			}
		}
	}
	if counts["anthropic_snapshots"] != 0 || counts["anthropic_quota_values"] != 0 {
		t.Fatalf("export escaped source snapshot: %v", counts)
	}
}
