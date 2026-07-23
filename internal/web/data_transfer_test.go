package web

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func newDataTransferHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "onwatch.db"))
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewHandler(s, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil), s
}

func TestDataExportHandlerDownloadsZIP(t *testing.T) {
	h, s := newDataTransferHandler(t)
	renewsAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	if _, err := s.InsertSnapshot(&api.Snapshot{
		CapturedAt: time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		Sub:        api.QuotaInfo{Limit: 1000, Requests: 10, RenewsAt: renewsAt},
		Search:     api.QuotaInfo{Limit: 100, Requests: 2, RenewsAt: renewsAt},
		ToolCall:   api.QuotaInfo{Limit: 50, Requests: 1, RenewsAt: renewsAt},
	}); err != nil {
		t.Fatalf("InsertSnapshot: %v", err)
	}

	recorder := httptest.NewRecorder()
	h.ExportData(recorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(disposition, ".onwatch.zip") {
		t.Fatalf("content disposition = %q", disposition)
	}
	if _, err := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len())); err != nil {
		t.Fatalf("response is not a ZIP: %v", err)
	}

	methodRecorder := httptest.NewRecorder()
	h.ExportData(methodRecorder, httptest.NewRequest(http.MethodPost, "/api/data/export", nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST export status = %d", methodRecorder.Code)
	}
}

func TestDataImportHandlerMergesArchive(t *testing.T) {
	sourceHandler, _ := newDataTransferHandler(t)
	exportRecorder := httptest.NewRecorder()
	sourceHandler.ExportData(exportRecorder, httptest.NewRequest(http.MethodGet, "/api/data/export", nil))
	if exportRecorder.Code != http.StatusOK {
		t.Fatalf("source export status = %d", exportRecorder.Code)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "source.onwatch.zip")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(exportRecorder.Body.Bytes()); err != nil {
		t.Fatalf("write archive part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	destinationHandler, _ := newDataTransferHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/data/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	destinationHandler.ImportData(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"total"`) {
		t.Fatalf("response missing summary: %s", recorder.Body.String())
	}

	badMethod := httptest.NewRecorder()
	destinationHandler.ImportData(badMethod, httptest.NewRequest(http.MethodGet, "/api/data/import", nil))
	if badMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET import status = %d", badMethod.Code)
	}
	missingFile := httptest.NewRecorder()
	emptyRequest := httptest.NewRequest(http.MethodPost, "/api/data/import", strings.NewReader(""))
	emptyRequest.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	destinationHandler.ImportData(missingFile, emptyRequest)
	if missingFile.Code != http.StatusBadRequest {
		t.Fatalf("missing file status = %d", missingFile.Code)
	}
}

func TestSettingsPageIncludesDataTransferControls(t *testing.T) {
	h, _ := newDataTransferHandler(t)
	recorder := httptest.NewRecorder()
	h.SettingsPage(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-tab="data"`,
		`id="panel-data"`,
		`id="data-export-btn"`,
		`id="data-import-input"`,
		`accept=".onwatch.zip"`,
		`multiple`,
		`id="data-import-result"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("settings page missing %s", expected)
		}
	}
}
