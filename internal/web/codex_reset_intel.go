package web

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

const (
	defaultResetTrackerURL  = "https://codexresets.com/"
	defaultOpenAIPageURL    = "https://status.openai.com/proxy/status.openai.com"
	defaultOpenAIEventsURL  = "https://status.openai.com/proxy/status.openai.com/incidents"
	resetIntelCacheDuration = 2 * time.Minute
	resetIntelMaxBody       = 4 << 20
)

type codexResetIntelService struct {
	store      *store.Store
	client     *http.Client
	trackerURL string
	pageURL    string
	eventsURL  string

	mu       sync.Mutex
	cachedAt time.Time
	cached   codexPublicIntel
}

type codexPublicIntel struct {
	Global  resetTrackerIntel `json:"global"`
	Service serviceIntel      `json:"service"`
	Errors  []string          `json:"errors,omitempty"`
}

type resetTrackerIntel struct {
	Signal          string    `json:"signal"`
	SignalLabel     string    `json:"signalLabel"`
	SignalText      string    `json:"signalText,omitempty"`
	SignalAt        time.Time `json:"signalAt,omitempty"`
	SignalSourceURL string    `json:"signalSourceUrl,omitempty"`
	LastResetAt     time.Time `json:"lastResetAt"`
	LastResetText   string    `json:"lastResetText"`
	LastResetURL    string    `json:"lastResetUrl"`
	TrackerSyncAt   time.Time `json:"trackerSyncAt,omitempty"`
	TrackerURL      string    `json:"trackerUrl"`
}

type serviceIntel struct {
	Status        string         `json:"status"`
	Label         string         `json:"label"`
	IncidentCount int            `json:"incidentCount"`
	Incidents     []serviceEvent `json:"incidents"`
	SourceURL     string         `json:"sourceUrl"`
}

type serviceEvent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Severity    string    `json:"severity"`
	PublishedAt time.Time `json:"publishedAt"`
	Components  []string  `json:"components"`
}

type accountResetEvidence struct {
	Status        string        `json:"status"`
	Label         string        `json:"label"`
	Explanation   string        `json:"explanation"`
	BeforeAt      *time.Time    `json:"beforeAt,omitempty"`
	AfterAt       *time.Time    `json:"afterAt,omitempty"`
	MeterDropped  bool          `json:"meterDropped"`
	WindowChanged bool          `json:"windowChanged"`
	Changes       []quotaChange `json:"changes"`
}

type quotaChange struct {
	Name   string  `json:"name"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
}

func newCodexResetIntelService(st *store.Store) *codexResetIntelService {
	return &codexResetIntelService{
		store:      st,
		client:     &http.Client{Timeout: 8 * time.Second},
		trackerURL: defaultResetTrackerURL,
		pageURL:    defaultOpenAIPageURL,
		eventsURL:  defaultOpenAIEventsURL,
	}
}

func (h *Handler) CodexResetIntelligence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.resetIntel == nil {
		respondError(w, http.StatusServiceUnavailable, "reset intelligence unavailable")
		return
	}
	public := h.resetIntel.public()
	response := map[string]any{
		"fetchedAt": time.Now().UTC(),
		"global":    public.Global,
		"service":   public.Service,
		"errors":    public.Errors,
		"account":   h.resetIntel.accountEvidence(parseCodexAccountID(r), public.Global.LastResetAt),
	}
	respondJSON(w, http.StatusOK, response)
}

func (s *codexResetIntelService) public() codexPublicIntel {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < resetIntelCacheDuration {
		return s.cached
	}

	var result codexPublicIntel
	tracker, err := s.fetchTracker()
	if err != nil {
		result.Errors = append(result.Errors, "Public reset feed unavailable")
	} else {
		result.Global = tracker
	}
	service, err := s.fetchService()
	if err != nil {
		result.Errors = append(result.Errors, "OpenAI status feed unavailable")
		result.Service = serviceIntel{Status: "unknown", Label: "Status unavailable", SourceURL: "https://status.openai.com/"}
	} else {
		result.Service = service
	}
	s.cachedAt = time.Now()
	s.cached = result
	return result
}

var (
	resetHeroTimeRE = regexp.MustCompile(`(?s)class="hero-figure"[^>]*data-datetime="([^"]+)"`)
	resetHeroTextRE = regexp.MustCompile(`(?s)class="hero-tweet"[^>]*>(.*?)</p>`)
	resetHeroLinkRE = regexp.MustCompile(`(?s)class="hero-link"[^>]*href="([^"]+)"`)
	resetTodayRE    = regexp.MustCompile(`(?s)class="reset-today-card([^\"]*)".*?class="reset-badge[^\"]*"[^>]*>(.*?)</span>.*?class="reset-today-text"[^>]*>(.*?)</p>.*?class="reset-today-ts"[^>]*>(.*?)</span>.*?class="reset-today-link"[^>]*href="([^"]+)"`)
	resetSyncRE     = regexp.MustCompile(`(?s)last_sync:\s*([^<]+)`)
	tagRE           = regexp.MustCompile(`<[^>]+>`)
)

func (s *codexResetIntelService) fetchTracker() (resetTrackerIntel, error) {
	body, err := s.fetch(s.trackerURL)
	if err != nil {
		return resetTrackerIntel{}, err
	}
	result := resetTrackerIntel{TrackerURL: s.trackerURL, Signal: "none", SignalLabel: "No reset today"}
	if match := resetHeroTimeRE.FindSubmatch(body); len(match) == 2 {
		result.LastResetAt, err = time.Parse(time.RFC3339Nano, string(match[1]))
		if err != nil {
			return result, fmt.Errorf("parse last reset: %w", err)
		}
	} else {
		return result, fmt.Errorf("last reset timestamp missing")
	}
	if match := resetHeroTextRE.FindSubmatch(body); len(match) == 2 {
		result.LastResetText = cleanHTML(match[1])
	}
	if match := resetHeroLinkRE.FindSubmatch(body); len(match) == 2 {
		result.LastResetURL = html.UnescapeString(string(match[1]))
	}
	if match := resetTodayRE.FindSubmatch(body); len(match) == 6 {
		classes := strings.ToLower(string(match[1]))
		switch {
		case strings.Contains(classes, "possible"):
			result.Signal = "possible"
		case strings.Contains(classes, "yes"), strings.Contains(strings.ToLower(cleanHTML(match[2])), "reset"):
			result.Signal = "confirmed"
		}
		result.SignalLabel = cleanHTML(match[2])
		result.SignalText = cleanHTML(match[3])
		result.SignalAt, _ = time.Parse("2006-01-02 15:04:05 MST", cleanHTML(match[4]))
		result.SignalSourceURL = html.UnescapeString(string(match[5]))
	}
	if match := resetSyncRE.FindSubmatch(body); len(match) == 2 {
		result.TrackerSyncAt, _ = time.Parse(time.RFC1123, strings.TrimSpace(cleanHTML(match[1])))
	}
	return result, nil
}

func cleanHTML(value []byte) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(string(value), "")))
}

type statusPagePayload struct {
	Summary struct {
		Structure struct {
			Items []struct {
				Group *struct {
					Name       string `json:"name"`
					Components []struct {
						ID   string `json:"component_id"`
						Name string `json:"name"`
					} `json:"components"`
				} `json:"group"`
			} `json:"items"`
		} `json:"structure"`
	} `json:"summary"`
}

type statusEventsPayload struct {
	Incidents []struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Status      string    `json:"status"`
		PublishedAt time.Time `json:"published_at"`
		Affected    []struct {
			ID            string `json:"component_id"`
			Status        string `json:"status"`
			CurrentStatus string `json:"current_status"`
		} `json:"affected_components"`
		Updates []struct {
			Text string `json:"message_string"`
		} `json:"updates"`
	} `json:"incidents"`
}

func (s *codexResetIntelService) fetchService() (serviceIntel, error) {
	pageBody, err := s.fetch(s.pageURL)
	if err != nil {
		return serviceIntel{}, err
	}
	eventsBody, err := s.fetch(s.eventsURL)
	if err != nil {
		return serviceIntel{}, err
	}
	var page statusPagePayload
	var events statusEventsPayload
	if err := json.Unmarshal(pageBody, &page); err != nil {
		return serviceIntel{}, err
	}
	if err := json.Unmarshal(eventsBody, &events); err != nil {
		return serviceIntel{}, err
	}

	targets := map[string]string{}
	for _, item := range page.Summary.Structure.Items {
		if item.Group == nil {
			continue
		}
		for _, component := range item.Group.Components {
			if item.Group.Name == "Codex" || component.Name == "ChatGPT Work" || component.Name == "Codex in ChatGPT Desktop" {
				targets[component.ID] = component.Name
			}
		}
	}
	result := serviceIntel{Status: "operational", Label: "No active Codex incident", SourceURL: "https://status.openai.com/", Incidents: []serviceEvent{}}
	for _, incident := range events.Incidents {
		if strings.EqualFold(incident.Status, "resolved") {
			continue
		}
		matched := []string{}
		severity := "operational"
		for _, affected := range incident.Affected {
			if name, ok := targets[affected.ID]; ok {
				matched = append(matched, name)
				componentStatus := affected.Status
				if componentStatus == "" {
					componentStatus = affected.CurrentStatus
				}
				severity = worseSeverity(severity, componentStatus)
			}
		}
		if len(matched) == 0 && !strings.Contains(strings.ToLower(incident.Name), "codex") && !strings.Contains(strings.ToLower(incident.Name), "chatgpt work") {
			continue
		}
		sort.Strings(matched)
		result.Incidents = append(result.Incidents, serviceEvent{ID: incident.ID, Name: incident.Name, Status: incident.Status, Severity: severity, PublishedAt: incident.PublishedAt, Components: matched})
		result.Status = worseSeverity(result.Status, severity)
	}
	result.IncidentCount = len(result.Incidents)
	if result.IncidentCount > 0 {
		result.Label = fmt.Sprintf("%d active Codex incident", result.IncidentCount)
		if result.IncidentCount != 1 {
			result.Label += "s"
		}
	}
	return result, nil
}

func worseSeverity(left, right string) string {
	rank := map[string]int{"unknown": -1, "operational": 0, "degraded_performance": 1, "partial_outage": 2, "full_outage": 3}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func (s *codexResetIntelService) fetch(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "onWatch/reset-intelligence")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, resetIntelMaxBody))
}

func (s *codexResetIntelService) accountEvidence(accountID int64, eventAt time.Time) accountResetEvidence {
	missing := accountResetEvidence{Status: "unavailable", Label: "No local evidence", Explanation: "OnWatch needs samples before and after the announcement.", Changes: []quotaChange{}}
	if s.store == nil || eventAt.IsZero() {
		return missing
	}
	snapshots, err := s.store.QueryCodexRange(accountID, eventAt.Add(-2*time.Hour), eventAt.Add(4*time.Hour))
	if err != nil {
		return missing
	}
	var before *api.CodexSnapshot
	var afterCandidates []*api.CodexSnapshot
	for _, snapshot := range snapshots {
		if !snapshot.CapturedAt.After(eventAt) {
			before = snapshot
		} else {
			afterCandidates = append(afterCandidates, snapshot)
		}
	}
	if before == nil || len(afterCandidates) == 0 {
		return missing
	}
	after := afterCandidates[len(afterCandidates)-1]
	for _, candidate := range afterCandidates {
		if snapshotShowsReset(before, candidate) {
			after = candidate
			break
		}
	}
	evidence := accountResetEvidence{Status: "not-observed", Label: "No meter reset observed", Explanation: "The local samples do not show a clear quota drop or reset-window change near the announcement.", BeforeAt: &before.CapturedAt, AfterAt: &after.CapturedAt, Changes: []quotaChange{}}
	beforeByName := map[string]api.CodexQuota{}
	for _, quota := range before.Quotas {
		beforeByName[quota.Name] = quota
	}
	for _, quota := range after.Quotas {
		previous, ok := beforeByName[quota.Name]
		if !ok {
			continue
		}
		evidence.Changes = append(evidence.Changes, quotaChange{Name: api.CodexDisplayName(quota.Name), Before: previous.Utilization, After: quota.Utilization})
		if previous.Utilization-quota.Utilization >= 10 {
			evidence.MeterDropped = true
		}
		if previous.ResetsAt != nil && quota.ResetsAt != nil && !previous.ResetsAt.Equal(*quota.ResetsAt) {
			evidence.WindowChanged = true
		}
	}
	if evidence.MeterDropped || evidence.WindowChanged {
		evidence.Status = "observed"
		evidence.Label = "Account change observed"
		evidence.Explanation = "OnWatch recorded a quota drop or reset-window change near the public announcement. Timing supports the link but does not prove causation."
	}
	return evidence
}

func snapshotShowsReset(before, after *api.CodexSnapshot) bool {
	beforeByName := map[string]api.CodexQuota{}
	for _, quota := range before.Quotas {
		beforeByName[quota.Name] = quota
	}
	for _, quota := range after.Quotas {
		previous, ok := beforeByName[quota.Name]
		if !ok {
			continue
		}
		if previous.Utilization-quota.Utilization >= 10 {
			return true
		}
		if previous.ResetsAt != nil && quota.ResetsAt != nil && !previous.ResetsAt.Equal(*quota.ResetsAt) {
			return true
		}
	}
	return false
}
