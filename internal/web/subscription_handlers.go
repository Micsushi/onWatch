package web

import (
	"encoding/json"
	"github.com/onllm-dev/onwatch/v2/internal/subscription"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SubscriptionValue is a read-only comparison over persisted observations.
func (h *Handler) SubscriptionValue(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider != "codex" && provider != "anthropic" && provider != "antigravity" {
		respondError(w, 400, "Unsupported subscription provider")
		return
	}
	if h.store == nil {
		respondError(w, 503, "Usage storage unavailable")
		return
	}
	end := time.Now().UTC()
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 62 {
			respondError(w, 400, "days must be 1 to 62")
			return
		}
		days = n
	}
	start := end.AddDate(0, 0, -days)
	for _, bound := range []struct {
		key    string
		target *time.Time
	}{{"start", &start}, {"end", &end}} {
		if v := r.URL.Query().Get(bound.key); v != "" {
			t, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				respondError(w, 400, "Invalid date")
				return
			}
			*bound.target = t
		}
	}
	if !end.After(start) || end.Sub(start) > 62*24*time.Hour {
		respondError(w, 400, "Choose a range of at most 62 days")
		return
	}
	profile := subscription.Profile{}
	key := "subscription_profile_" + provider
	accountID := parseCodexAccountID(r)
	if provider == "codex" {
		key += "_" + strconv.FormatInt(accountID, 10)
	}
	if saved, e := h.store.GetSetting(key); e == nil && saved != "" {
		_ = json.Unmarshal([]byte(saved), &profile)
	}
	if r.Method == http.MethodPut {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&profile); err != nil || profile.MonthlyUSD < 0 || profile.MonthlyUSD > 100000 || profile.Multiplier < 0 || profile.Multiplier > 10000 || math.IsNaN(profile.MonthlyUSD) || math.IsNaN(profile.Multiplier) || len(profile.Name) > 100 {
			respondError(w, 400, "Invalid plan profile")
			return
		}
		profile.Name = strings.TrimSpace(profile.Name)
		if profile.Name == "" && (profile.MonthlyUSD != 0 || profile.Multiplier != 0) {
			respondError(w, 400, "Enter a plan name")
			return
		}
		profile.Source = "configured monthly USD, before tax"
	} else if r.Method != http.MethodGet {
		respondError(w, 405, "Use GET or PUT")
		return
	}
	if profile.Name == "" {
		switch provider {
		case "codex":
			if latest, e := h.store.QueryLatestCodex(accountID); e == nil && latest != nil {
				switch latest.PlanType {
				case "pro":
					profile = subscription.Profile{Name: "Pro 20x", MonthlyUSD: 200, Multiplier: 20, Source: "Pro meter + published monthly list price; editable"}
				case "prolite":
					profile = subscription.Profile{Name: "Pro 5x", MonthlyUSD: 100, Multiplier: 5, Source: "Pro Lite meter + published monthly list price; editable"}
				case "plus":
					profile = subscription.Profile{Name: "Plus", MonthlyUSD: 20, Multiplier: 1, Source: "Plus meter + published monthly list price; editable"}
				}
			}
		case "antigravity":
			if latest, e := h.store.QueryLatestAntigravity(); e == nil && latest != nil && latest.PlanName == "Pro" {
				profile = subscription.Profile{Name: "Google AI Pro", MonthlyUSD: 20, Multiplier: 1, Source: "Pro meter + published monthly list price; editable"}
			}
		}
	}
	account := r.URL.Query().Get("usage_account")
	if account == "" {
		account = "default"
	}
	if len(account) > 256 {
		respondError(w, 400, "Invalid usage account")
		return
	}
	events, meters, err := h.store.SubscriptionInputs(r.Context(), provider, account, accountID, start, end)
	if err != nil {
		h.logger.Error("subscription comparison failed", "error", err)
		respondError(w, 500, "Could not load subscription observations; try a shorter range")
		return
	}
	// The current plan controls normalization, not which historical activity exists.
	plan := ""
	for _, m := range meters {
		if m.Source == "poll" && m.At.After(start) {
			plan = m.Plan
		}
	}
	if provider == "codex" {
		latest, e := h.store.QueryLatestCodex(accountID)
		if e == nil && latest != nil {
			plan = latest.PlanType
		}
	}
	profile.PlanType = plan
	if r.Method == http.MethodPut {
		b, err := json.Marshal(profile)
		if err != nil {
			respondError(w, 400, "Invalid plan profile")
			return
		}
		if err := h.store.SetSetting(key, string(b)); err != nil {
			respondError(w, 500, "Could not save plan")
			return
		}
	}
	result := subscription.Analyze(events, meters, profile)
	var latestMeter *time.Time
	for _, meter := range meters {
		if latestMeter == nil || meter.At.After(*latestMeter) {
			at := meter.At
			latestMeter = &at
		}
	}
	meterStale := latestMeter != nil && end.Sub(*latestMeter) > 30*time.Minute
	result.Warnings = append(result.Warnings, "Period value uses the configured current monthly fee and can span historical plan or model changes. Historical allowances with a different plan are not normalized by the current multiplier.")
	result.Warnings = append(result.Warnings, "Token account '"+account+"' is paired with the selected meter. Legacy records lack a verified account binding; device labels describe source paths, not proof of machine-specific limits.")
	respondJSON(w, 200, map[string]any{"provider": provider, "start": start, "end": end, "latest_meter_at": latestMeter, "meter_stale": meterStale, "basis": "Current API Standard short-context text rates, checked 2026-09-07. Excludes tools and API speed/context premiums; Codex credits shown separately.", "report": result})
}
