package collector

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/ingest"
)

func TestAntigravityAccountBinding(t *testing.T) {
	reset := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, source, binding, cliEmail, ideEmail string
		cliFailure, wantError                     bool
		calls                                     []string
	}{
		{"matching opaque account", "cli", "synthetic@example.invalid", "synthetic@example.invalid", "", false, false, []string{"cli"}},
		{"outer spaces", "cli", " synthetic@example.invalid ", " synthetic@example.invalid ", "", false, false, []string{"cli"}},
		{"missing binding before fetch", "auto", "", "", "", false, true, nil},
		{"invalid binding before fetch", "auto", "Name <synthetic@example.invalid>", "", "", false, true, nil},
		{"missing CLI identity", "auto", "synthetic@example.invalid", "", "synthetic@example.invalid", false, true, []string{"cli"}},
		{"wrong CLI identity no fallback", "auto", "synthetic@example.invalid", "other@example.invalid", "synthetic@example.invalid", false, true, []string{"cli"}},
		{"case mismatch", "cli", "synthetic@example.invalid", "Synthetic@example.invalid", "", false, true, []string{"cli"}},
		{"transport fallback matching IDE", "auto", "synthetic@example.invalid", "", "synthetic@example.invalid", true, false, []string{"cli", "ide"}},
		{"transport fallback missing IDE identity", "auto", "synthetic@example.invalid", "", "", true, true, []string{"cli", "ide"}},
		{"transport fallback wrong IDE identity", "auto", "synthetic@example.invalid", "", "other@example.invalid", true, true, []string{"cli", "ide"}},
		{"explicit IDE matching", "ide", "synthetic@example.invalid", "", "synthetic@example.invalid", false, false, []string{"ide"}},
		{"explicit IDE mismatch", "ide", "synthetic@example.invalid", "", "other@example.invalid", false, true, []string{"ide"}},
		{"explicit CLI transport failure", "cli", "synthetic@example.invalid", "", "synthetic@example.invalid", true, true, []string{"cli"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTIGRAVITY_SOURCE", tc.source)
			var calls []string
			r := &Runtime{antigravityFetch: func(_ context.Context, source string) (*api.AntigravitySnapshot, error) {
				calls = append(calls, source)
				if source == "cli" && tc.cliFailure {
					return nil, errors.New("synthetic transport failure")
				}
				email := tc.cliEmail
				if source == "ide" {
					email = tc.ideEmail
				}
				return &api.AntigravitySnapshot{Email: email, PlanName: "synthetic-plan", SummaryGroups: []api.AntigravityQuotaSummaryGroup{{GroupKey: "gemini", Buckets: []api.AntigravityQuotaSummaryBucket{{BucketID: "weekly", Window: "weekly", UsagePercent: 48.1, ResetTime: &reset}, {BucketID: "5h", Window: "5h", UsagePercent: 0}}}}}, nil
			}}
			assignment := ingest.ProviderAssignment{Provider: "antigravity", ExternalID: "opaque-acct-42", CredentialAlias: "UNCHANGED_ALIAS", AntigravityAccountEmail: tc.binding}
			before := assignment
			event, err := r.pollQuota(context.Background(), assignment)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
			if !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("fetches=%v want=%v", calls, tc.calls)
			}
			if assignment != before {
				t.Fatal("assignment changed")
			}
			if err != nil {
				if event.EventID != "" || event.Payload != nil {
					t.Fatal("failed poll emitted quota")
				}
				if strings.Contains(err.Error(), "@") {
					t.Fatal("error discloses identity")
				}
				return
			}
			if event.Account.ExternalID != "opaque-acct-42" || event.Provider != "antigravity" {
				t.Fatal("opaque account attribution changed")
			}
			var snapshot ingest.QuotaSnapshot
			if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Plan != "synthetic-plan" || len(snapshot.Metrics) != 2 {
				t.Fatal("quota payload changed")
			}
			weekly, short := snapshot.Metrics[0], snapshot.Metrics[1]
			if weekly.Group != "gemini" || weekly.Window != "weekly" || weekly.Value != 48.1 || weekly.Unit != "percent" || weekly.Limit != nil || !weekly.ResetsAt.Equal(reset) {
				t.Fatal("weekly fields lost")
			}
			if short.ResetsAt != nil || short.Value != 0 || short.Window != "5h" {
				t.Fatal("missing reset or zero quota changed")
			}
			if strings.Contains(string(event.Payload), "@") {
				t.Fatal("identity leaked into quota payload")
			}
		})
	}
}
