package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BackupMetadata struct {
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Database  string    `json:"database"`
}

func (s *Store) Backup(destination string) (BackupMetadata, error) {
	var metadata BackupMetadata
	if s == nil || s.db == nil || strings.TrimSpace(destination) == "" {
		return metadata, fmt.Errorf("backup destination is required")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return metadata, err
	}
	source, _ := filepath.Abs(s.dbPath)
	if source == destination {
		return metadata, fmt.Errorf("backup destination must differ from the live database")
	}
	if _, err := os.Stat(destination); err == nil {
		return metadata, fmt.Errorf("backup destination already exists")
	} else if !os.IsNotExist(err) {
		return metadata, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return metadata, err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".onwatch-backup-*.db")
	if err != nil {
		return metadata, err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		return metadata, err
	}
	_ = os.Remove(tempPath)
	defer os.Remove(tempPath)
	escaped := strings.ReplaceAll(tempPath, "'", "''")
	if _, err := s.db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return metadata, fmt.Errorf("create online SQLite backup: %w", err)
	}
	if err := os.Chmod(tempPath, 0600); err != nil {
		return metadata, fmt.Errorf("protect backup: %w", err)
	}
	backupDB, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return metadata, err
	}
	var integrity string
	err = backupDB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)
	closeErr := backupDB.Close()
	if err != nil || integrity != "ok" {
		return metadata, fmt.Errorf("backup integrity check failed: %s: %w", integrity, err)
	}
	if closeErr != nil {
		return metadata, closeErr
	}
	data, err := os.ReadFile(tempPath)
	if err != nil {
		return metadata, err
	}
	digest := sha256.Sum256(data)
	info, err := os.Stat(tempPath)
	if err != nil {
		return metadata, err
	}
	backupFile, err := os.OpenFile(tempPath, os.O_RDONLY, 0)
	if err != nil {
		return metadata, err
	}
	if err := backupFile.Sync(); err != nil {
		backupFile.Close()
		return metadata, err
	}
	if err := backupFile.Close(); err != nil {
		return metadata, err
	}
	metadata = BackupMetadata{CreatedAt: time.Now().UTC(), SHA256: hex.EncodeToString(digest[:]), Size: info.Size(), Database: filepath.Base(destination)}
	encoded, _ := json.MarshalIndent(metadata, "", "  ")
	metadataPath := destination + ".json"
	metadataTemp, err := os.CreateTemp(filepath.Dir(destination), ".onwatch-backup-metadata-*.json")
	if err != nil {
		return metadata, err
	}
	metadataTempPath := metadataTemp.Name()
	defer os.Remove(metadataTempPath)
	if err := metadataTemp.Chmod(0600); err != nil {
		metadataTemp.Close()
		return metadata, err
	}
	if _, err := metadataTemp.Write(append(encoded, '\n')); err != nil {
		metadataTemp.Close()
		return metadata, err
	}
	if err := metadataTemp.Sync(); err != nil {
		metadataTemp.Close()
		return metadata, err
	}
	if err := metadataTemp.Close(); err != nil {
		return metadata, err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return metadata, err
	}
	if err := os.Rename(metadataTempPath, metadataPath); err != nil {
		_ = os.Remove(destination)
		return metadata, fmt.Errorf("write backup metadata: %w", err)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil {
		_ = os.Remove(destination)
		_ = os.Remove(metadataPath)
		return metadata, fmt.Errorf("sync backup directory: %w", err)
	}
	return metadata, nil
}
