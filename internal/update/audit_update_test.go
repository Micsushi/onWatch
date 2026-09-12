package update

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAuditFailedRenamePreservesInstalled(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "onwatch")
	tmp := filepath.Join(dir, "new")
	if err := os.WriteFile(exe, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	original := renameUpdateFile
	defer func() { renameUpdateFile = original }()
	renameUpdateFile = func(from, to string) error {
		if from == tmp {
			return fmt.Errorf("fixture failure")
		}
		return os.Rename(from, to)
	}
	if err := replaceBinary(exe, tmp, slog.Default()); err == nil {
		t.Fatal("rename failure ignored")
	}
	data, err := os.ReadFile(exe)
	if err != nil || string(data) != "old" {
		t.Fatalf("installed binary lost: %v", err)
	}
	if runtime.GOOS == "windows" {
		renameUpdateFile = func(from, to string) error {
			if from == tmp || strings.Contains(from, ".old-") {
				return fmt.Errorf("fixture rollback failure")
			}
			return os.Rename(from, to)
		}
		if err := replaceBinary(exe, tmp, slog.Default()); err == nil {
			t.Fatal("rollback failure ignored")
		}
		backups, _ := filepath.Glob(exe + ".old-*")
		if len(backups) != 1 {
			t.Fatalf("missing recovery file: %v", backups)
		}
		data, err := os.ReadFile(backups[0])
		if err != nil || string(data) != "old" {
			t.Fatal("recovery data lost")
		}
	}
}
func TestAuditChecksumRejectsAlteredDownload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(file, []byte("expected"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("expected"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "%x  onwatch-windows-amd64.exe\n", sum) }))
	defer server.Close()
	u := NewUpdater("1.0.0", nil)
	url := server.URL + "/onwatch-windows-amd64.exe"
	if err := u.verifyChecksum(url, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := u.verifyChecksum(url, file); err == nil {
		t.Fatal("altered download accepted")
	}
}
