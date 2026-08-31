package collector

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

type cursorState struct {
	Offsets      map[string]int64 `json:"offsets"`
	LastUploadAt *time.Time       `json:"last_upload_at,omitempty"`
	LastError    string           `json:"last_error,omitempty"`
}
type SpoolRecord struct {
	Event     ingest.Event
	File      string
	EndOffset int64
}
type SpoolStatus struct {
	QueueBytes     int64      `json:"queue_bytes"`
	DiskBytes      int64      `json:"disk_bytes"`
	PendingEvents  int        `json:"pending_events"`
	OldestQueuedAt *time.Time `json:"oldest_queued_at,omitempty"`
	LastUploadAt   *time.Time `json:"last_upload_at,omitempty"`
	Warning        bool       `json:"warning"`
	Full           bool       `json:"full"`
	LastError      string     `json:"last_error,omitempty"`
}

type Spool struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	state    cursorState
}

func NewSpool(dir string, maxBytes int64) (*Spool, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	spool := &Spool{dir: dir, maxBytes: maxBytes, state: cursorState{Offsets: map[string]int64{}}}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err := json.Unmarshal(data, &spool.state); err != nil {
			return nil, fmt.Errorf("decode spool state: %w", err)
		}
	}
	if spool.state.Offsets == nil {
		spool.state.Offsets = map[string]int64{}
	}
	return spool, nil
}

func (s *Spool) Append(event ingest.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	status, err := s.statusLocked()
	if err != nil {
		return err
	}
	if status.DiskBytes+int64(len(encoded)) > s.maxBytes {
		return fmt.Errorf("collector spool full; upload must recover before collection resumes")
	}
	path := filepath.Join(s.dir, "events-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Spool) Batch(limit int, maxBytes int64) ([]SpoolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := s.eventFiles()
	if err != nil {
		return nil, err
	}
	var records []SpoolRecord
	var total int64
	for _, name := range files {
		path := filepath.Join(s.dir, name)
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		offset := s.state.Offsets[name]
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, err
		}
		reader := bufio.NewReader(file)
		position := offset
		for len(records) < limit {
			line, err := reader.ReadBytes('\n')
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				file.Close()
				return nil, err
			}
			position += int64(len(line))
			if total+int64(len(line)) > maxBytes && len(records) > 0 {
				break
			}
			var event ingest.Event
			if json.Unmarshal(line, &event) != nil {
				position -= int64(len(line))
				break
			}
			records = append(records, SpoolRecord{Event: event, File: name, EndOffset: position})
			total += int64(len(line))
		}
		_ = file.Close()
		if len(records) >= limit || total >= maxBytes {
			break
		}
	}
	return records, nil
}

func (s *Spool) Ack(records []SpoolRecord, lastUpload time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		if record.EndOffset > s.state.Offsets[record.File] {
			s.state.Offsets[record.File] = record.EndOffset
		}
	}
	s.state.LastUploadAt = &lastUpload
	s.state.LastError = ""
	if err := s.saveStateLocked(); err != nil {
		return err
	}
	return s.removeAckedClosedFilesLocked()
}

func (s *Spool) SetError(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastError = message
	return s.saveStateLocked()
}
func (s *Spool) Status() (SpoolStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Spool) statusLocked() (SpoolStatus, error) {
	status := SpoolStatus{LastUploadAt: s.state.LastUploadAt, LastError: s.state.LastError}
	files, err := s.eventFiles()
	if err != nil {
		return status, err
	}
	for _, name := range files {
		info, err := os.Stat(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		offset := s.state.Offsets[name]
		status.DiskBytes += info.Size()
		if offset > info.Size() {
			offset = 0
		}
		status.QueueBytes += info.Size() - offset
		file, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		_, _ = file.Seek(offset, io.SeekStart)
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), ingest.MaxEventBytes+4096)
		for scanner.Scan() {
			var event ingest.Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				status.PendingEvents++
				if status.OldestQueuedAt == nil || event.CapturedAt.Before(*status.OldestQueuedAt) {
					captured := event.CapturedAt
					status.OldestQueuedAt = &captured
				}
			}
		}
		_ = file.Close()
	}
	status.Full = status.DiskBytes >= s.maxBytes
	status.Warning = status.DiskBytes >= s.maxBytes*80/100 || (status.OldestQueuedAt != nil && time.Since(*status.OldestQueuedAt) > 24*time.Hour)
	return status, nil
}

func (s *Spool) eventFiles() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "events-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
func (s *Spool) saveStateLocked() error {
	encoded, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.dir, ".state-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func (s *Spool) removeAckedClosedFilesLocked() error {
	today := "events-" + time.Now().UTC().Format("2006-01-02") + ".jsonl"
	files, err := s.eventFiles()
	if err != nil {
		return err
	}
	for _, name := range files {
		if name == today {
			continue
		}
		info, err := os.Stat(filepath.Join(s.dir, name))
		if err == nil && s.state.Offsets[name] >= info.Size() {
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				return err
			}
			delete(s.state.Offsets, name)
		}
	}
	return s.saveStateLocked()
}
