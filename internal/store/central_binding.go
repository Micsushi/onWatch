package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

// ConfigureAntigravityBinding edits one existing assignment without migrating the
// database or reserializing unrelated JSON fields. Preview opens read-only.
// An empty email removes the binding, which makes the accepted collector fail closed.
func ConfigureAntigravityBinding(dbPath, deviceID, account, email string, expected int64, apply bool) (int64, error) {
	if expected < 0 || expected == math.MaxInt64 {
		return 0, fmt.Errorf("invalid expected revision")
	}
	if err := ingest.ValidateDeviceID(deviceID); err != nil {
		return 0, err
	}
	if account == "" || account != strings.TrimSpace(account) {
		return 0, fmt.Errorf("invalid account ID")
	}
	if email != "" {
		assignment := ingest.ProviderAssignment{AntigravityAccountEmail: email}
		if err := assignment.ValidateAntigravityBinding(); err != nil {
			return 0, err
		}
	}
	email = strings.TrimSpace(email)
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("an existing regular database file is required")
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	mode := "ro"
	if apply {
		mode = "rw"
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=" + mode}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var revision int64
	var original string
	err = db.QueryRow(`SELECT desired_config_revision, desired_config_json FROM devices WHERE device_id = ? AND revoked_at IS NULL`, deviceID).Scan(&revision, &original)
	if err != nil {
		return 0, fmt.Errorf("read active device configuration: %w", err)
	}
	if revision != expected {
		return 0, fmt.Errorf("configuration changed; read the current revision and preview again")
	}
	var ambiguous bool
	err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM json_tree(?) WHERE key IS NOT NULL GROUP BY parent, lower(key) HAVING count(*) > 1)`, original).Scan(&ambiguous)
	if err != nil || ambiguous {
		return 0, fmt.Errorf("invalid or ambiguous stored configuration JSON")
	}
	var config ingest.DesiredConfig
	if err := json.Unmarshal([]byte(original), &config); err != nil {
		return 0, fmt.Errorf("invalid stored configuration")
	}
	if config.Revision != revision {
		return 0, fmt.Errorf("stored configuration revisions disagree")
	}
	if err := ingest.ValidateDesiredConfig(config); err != nil {
		return 0, err
	}
	index := -1
	for i, assignment := range config.Assignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Provider), "antigravity") && assignment.ExternalID == account {
			index = i
		}
	}
	if index < 0 {
		return 0, fmt.Errorf("existing Antigravity assignment not found")
	}
	field := fmt.Sprintf("$.assignments[%d].antigravity_account_email", index)
	var proposed string
	if email == "" {
		err = db.QueryRow(`SELECT json_set(json_remove(?, ?), '$.revision', ?)`, original, field, revision+1).Scan(&proposed)
	} else {
		err = db.QueryRow(`SELECT json_set(?, ?, ?, '$.revision', ?)`, original, field, email, revision+1).Scan(&proposed)
	}
	if err != nil {
		return 0, err
	}
	err = db.QueryRow(`SELECT EXISTS(SELECT 1 FROM json_tree(?) WHERE key IS NOT NULL GROUP BY parent, lower(key) HAVING count(*) > 1)`, proposed).Scan(&ambiguous)
	if err != nil || ambiguous {
		return 0, fmt.Errorf("noncanonical configuration keys; binding was not changed")
	}
	var nextConfig ingest.DesiredConfig
	if err := json.Unmarshal([]byte(proposed), &nextConfig); err != nil {
		return 0, err
	}
	config.Revision = revision + 1
	config.Assignments[index].AntigravityAccountEmail = email
	if !reflect.DeepEqual(config, nextConfig) {
		return 0, fmt.Errorf("configuration field preservation check failed")
	}
	if err := ingest.ValidateDesiredConfig(nextConfig); err != nil {
		return 0, err
	}
	if !apply {
		return revision + 1, nil
	}
	result, err := db.Exec(`UPDATE devices SET desired_config_revision = ?, desired_config_json = ?
		WHERE device_id = ? AND revoked_at IS NULL AND desired_config_revision = ? AND desired_config_json = ?`,
		revision+1, proposed, deviceID, expected, original)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, fmt.Errorf("configuration changed; read the current revision and preview again")
	}
	return revision + 1, nil
}
