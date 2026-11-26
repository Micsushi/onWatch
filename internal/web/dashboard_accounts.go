package web

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

type dashboardAccount struct {
	ID      string
	Name    string
	Email   string
	Checked bool
	Hidden  bool
	Source  store.AntigravityAccount
}

func accountEmail(value string) string {
	value = strings.TrimSpace(value)
	addr, err := mail.ParseAddress(value)
	if err != nil || len(value) > 254 || addr.Name != "" || addr.Address != value {
		return ""
	}
	return value
}

func accountEmailSetting(provider, id string) string {
	return "dashboard_account_email:" + provider + ":" + id
}

func (h *Handler) displayAccountEmail(provider, id, email string) string {
	if h.store != nil {
		if saved, err := h.store.GetSetting(accountEmailSetting(provider, id)); err == nil && accountEmail(saved) != "" {
			return saved
		}
	}
	return accountEmail(email)
}

func (h *Handler) codexDashboardAccountEmail(account store.ProviderAccount) string {
	var email string
	if h.store != nil && account.ExternalID != "" {
		email, _ = h.store.CentralCodexAccountLabel(account.ExternalID)
	}
	return h.displayAccountEmail("codex", strconv.FormatInt(account.ID, 10), email)
}

func (h *Handler) dashboardAccounts(provider string, codex []map[string]interface{}) []dashboardAccount {
	var accounts []dashboardAccount
	switch provider {
	case "codex":
		for _, profile := range codex {
			id, _ := profile["id"].(int64)
			name, _ := profile["name"].(string)
			email, _ := profile["email"].(string)
			hidden, _ := profile["hidden"].(bool)
			accounts = append(accounts, dashboardAccount{ID: strconv.FormatInt(id, 10), Name: name, Email: email, Hidden: hidden})
		}
	case "antigravity":
		if h.store == nil {
			return nil
		}
		sources, err := h.store.AntigravityAccounts()
		if err != nil {
			h.logger.Error("failed to list Antigravity accounts", "error", err)
			return nil
		}
		for index, source := range sources {
			identity := "central:" + source.ExternalID
			if source.ExternalID == "" {
				identity = "local:" + source.Email
			}
			digest := sha256.Sum256([]byte(identity))
			id := fmt.Sprintf("%x", digest[:16])
			accounts = append(accounts, dashboardAccount{
				ID: id, Name: fmt.Sprintf("Account %d", index+1), Source: source,
				Email: h.displayAccountEmail(provider, id, source.Email),
			})
		}
	}
	return accounts
}

func selectDashboardAccounts(accounts []dashboardAccount, requested []string) []dashboardAccount {
	wanted := make(map[string]bool, len(requested))
	for _, id := range requested {
		wanted[id] = true
	}
	var selected []dashboardAccount
	for i := range accounts {
		if wanted[accounts[i].ID] {
			accounts[i].Checked = true
			selected = append(selected, accounts[i])
		}
	}
	if len(selected) == 0 && len(accounts) > 0 {
		for i := range accounts {
			if !accounts[i].Hidden {
				accounts[i].Checked = true
				selected = append(selected, accounts[i])
				break
			}
		}
	}
	return selected
}

func (h *Handler) antigravityDashboardAccount(id string) (dashboardAccount, bool) {
	for _, account := range h.dashboardAccounts("antigravity", nil) {
		if account.ID == id {
			return account, true
		}
	}
	return dashboardAccount{}, false
}

func (h *Handler) latestAntigravityDashboardAccount(account dashboardAccount) (*api.AntigravitySnapshot, error) {
	samples, err := h.store.QueryAntigravityAccountRange(account.Source, time.Unix(0, 0), time.Now().UTC().Add(time.Minute), 1)
	if err != nil || len(samples) == 0 {
		return nil, err
	}
	return samples[len(samples)-1], nil
}

func (h *Handler) DashboardAccountEmail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondError(w, http.StatusMethodNotAllowed, "Use PUT")
		return
	}
	if h.store == nil {
		respondError(w, http.StatusServiceUnavailable, "Account storage unavailable")
		return
	}
	var input struct{ Provider, Account, Email string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil ||
		(strings.TrimSpace(input.Email) != "" && accountEmail(input.Email) == "") {
		respondError(w, http.StatusBadRequest, "Enter a valid email address")
		return
	}
	var codex []map[string]interface{}
	if input.Provider == "codex" {
		codex = h.codexDashboardProfiles(h.providerVisibilityMap(), true)
	}
	for _, account := range h.dashboardAccounts(input.Provider, codex) {
		if account.ID != input.Account {
			continue
		}
		if err := h.store.SetSetting(accountEmailSetting(input.Provider, account.ID), accountEmail(input.Email)); err != nil {
			respondError(w, http.StatusInternalServerError, "Could not save account label")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"saved": true})
		return
	}
	respondError(w, http.StatusNotFound, "Account unavailable")
}
