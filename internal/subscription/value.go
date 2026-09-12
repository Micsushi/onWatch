// Package subscription compares observed quota movement with local API-equivalent usage.
package subscription

import (
	"math"
	"sort"
	"strings"
	"time"
)

type Profile struct {
	PlanType   string  `json:"planType,omitempty"`
	Name       string  `json:"name"`
	MonthlyUSD float64 `json:"monthlyUsd"`
	Multiplier float64 `json:"multiplier"`
	Source     string  `json:"source"`
}
type Event struct {
	At                                      time.Time
	End                                     time.Time
	Model, Effort, Speed, Device, Plan      string
	Tokens, Input, Cached, Output, Requests int
	Cost, Credits                           float64
	Priced, CreditsKnown, Archived          bool
}
type Meter struct {
	At     time.Time `json:"at"`
	Reset  time.Time `json:"reset"`
	Used   float64   `json:"used"`
	Quota  string    `json:"quota"`
	Plan   string    `json:"plan"`
	Source string    `json:"source"`
}
type Point struct {
	At   time.Time `json:"at"`
	Used float64   `json:"used"`
	Cost float64   `json:"cost"`
}
type Cycle struct {
	UnmatchedPoints float64   `json:"unmatchedPoints"`
	ArchiveTiming   bool      `json:"archiveTiming"`
	Models          []Model   `json:"models"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	Reset           time.Time `json:"reset"`
	Quota           string    `json:"quota"`
	Plan            string    `json:"plan"`
	StartUsed       float64   `json:"startUsed"`
	EndUsed         float64   `json:"endUsed"`
	Used            float64   `json:"used"`
	Cost            float64   `json:"cost"`
	UnknownRequests int       `json:"unknownRequests"`
	Complete        bool      `json:"complete"`
	Boundary        string    `json:"boundary"`
	Per100          *float64  `json:"per100"`
	X1              *float64  `json:"x1"`
	Low             *float64  `json:"low"`
	High            *float64  `json:"high"`
	Points          []Point   `json:"points"`
}
type Model struct {
	Model           string   `json:"model"`
	Effort          string   `json:"effort"`
	Speed           string   `json:"speed"`
	Cost            float64  `json:"cost"`
	Credits         float64  `json:"credits"`
	CreditsUnknown  int      `json:"creditsUnknown"`
	Requests        int      `json:"requests"`
	UnknownRequests int      `json:"unknownRequests"`
	Tokens          int      `json:"tokens"`
	Input           int      `json:"input"`
	Cached          int      `json:"cached"`
	Output          int      `json:"output"`
	QuotaPoints     float64  `json:"quotaPoints"`
	MatchedCost     float64  `json:"matchedCost"`
	MatchedOutput   int      `json:"matchedOutput"`
	Per100          *float64 `json:"per100"`
	OutputPerPoint  *float64 `json:"outputPerPoint"`
}
type Device struct {
	Name     string  `json:"name"`
	Cost     float64 `json:"cost"`
	Requests int     `json:"requests"`
}
type Report struct {
	RejectedMeterCorrections int      `json:"rejectedMeterCorrections"`
	Profile                  Profile  `json:"profile"`
	Cost                     float64  `json:"cost"`
	Requests                 int      `json:"requests"`
	UnknownRequests          int      `json:"unknownRequests"`
	ArchivedRequests         int      `json:"archivedRequests"`
	ValueMultiple            *float64 `json:"valueMultiple"`
	Cycles                   []Cycle  `json:"cycles"`
	Models                   []Model  `json:"models"`
	Devices                  []Device `json:"devices"`
	Warnings                 []string `json:"warnings"`
}

func number(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
func modelKey(e Event) string { return e.Model + "|" + e.Effort + "|" + e.Speed }

func matchesQuota(e Event, quota string) bool {
	switch quota {
	case "five_hour", "seven_day", "weekly":
		return true
	case "seven_day_sonnet":
		return strings.HasPrefix(e.Model, "claude-") && strings.Contains(e.Model, "sonnet")
	case "seven_day_opus":
		return strings.HasPrefix(e.Model, "claude-") && strings.Contains(e.Model, "opus")
	}
	if strings.HasPrefix(quota, "antigravity_claude_gpt:") {
		return strings.HasPrefix(e.Model, "claude-") || strings.HasPrefix(e.Model, "gpt-")
	}
	if strings.HasPrefix(quota, "antigravity_gemini_pro:") {
		// The persisted summary key retains its legacy Pro name for the shared Gemini pool.
		return strings.HasPrefix(e.Model, "gemini-")
	}
	if strings.HasPrefix(quota, "antigravity_gemini_flash:") {
		return strings.HasPrefix(e.Model, "gemini-") && strings.Contains(e.Model, "flash")
	}
	// Extra-spend and unknown scoped meters are not a shared token allowance.
	return false
}

// Analyze does not allocate shared quota to models in proportion to their prices.
// A model gets calibration only from isolated >=5pp intervals of the weekly meter.
func Analyze(events []Event, meters []Meter, profile Profile) Report {
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	archiveEnds := make([]time.Time, len(events)+1)
	for i, e := range events {
		archiveEnds[i+1] = archiveEnds[i]
		if e.Archived && e.End.After(archiveEnds[i+1]) {
			archiveEnds[i+1] = e.End
		}
	}
	meters = preferRolloutMeters(meters)
	sort.SliceStable(meters, func(i, j int) bool {
		if meters[i].Quota != meters[j].Quota {
			return meters[i].Quota < meters[j].Quota
		}
		return meters[i].At.Before(meters[j].At)
	})
	r := Report{Profile: profile, Cycles: []Cycle{}, Models: []Model{}, Devices: []Device{}, Warnings: []string{}}
	models := map[string]*Model{}
	devices := map[string]*Device{}
	for _, e := range events {
		r.Cost += e.Cost
		r.Requests += e.Requests
		if !e.Priced {
			r.UnknownRequests += e.Requests
		}
		if e.Archived {
			r.ArchivedRequests += e.Requests
		}
		k := modelKey(e)
		m := models[k]
		if m == nil {
			m = &Model{Model: e.Model, Effort: e.Effort, Speed: e.Speed}
			models[k] = m
		}
		m.Cost += e.Cost
		m.Credits += e.Credits
		if !e.CreditsKnown {
			m.CreditsUnknown += e.Requests
		}
		m.Requests += e.Requests
		m.Tokens += e.Tokens
		m.Input += e.Input
		m.Cached += e.Cached
		m.Output += e.Output
		if !e.Priced {
			m.UnknownRequests += e.Requests
		}
		d := devices[e.Device]
		if d == nil {
			d = &Device{Name: e.Device}
			devices[e.Device] = d
		}
		d.Cost += e.Cost
		d.Requests += e.Requests
	}
	if profile.MonthlyUSD > 0 && r.Requests > r.UnknownRequests {
		r.ValueMultiple = number(r.Cost / profile.MonthlyUSD)
	}
	var cycle *Cycle
	var previous, anchor Meter
	boundary := "first observation"
	for index, m := range meters {
		if m.Used < 0 || m.Used > 100 || math.IsNaN(m.Used) {
			continue
		}
		if cycle != nil && m.Quota == previous.Quota && m.Plan == previous.Plan && math.Abs(m.Reset.Sub(previous.Reset).Seconds()) <= 60 && m.Used < previous.Used && previous.Used-m.Used <= 5 {
			r.RejectedMeterCorrections++
			continue
		}
		changed := cycle == nil || m.Quota != previous.Quota || m.Plan != previous.Plan || m.Used < previous.Used-0.001 || (!m.Reset.IsZero() && !previous.Reset.IsZero() && math.Abs(m.Reset.Sub(previous.Reset).Seconds()) > 60)
		if changed {
			if cycle != nil {
				boundary = "meter correction or reset"
				if m.Quota != previous.Quota {
					boundary = "first observation"
				} else if !previous.Reset.IsZero() && !m.At.Before(previous.Reset) {
					boundary = "scheduled boundary observed"
				} else if m.Used < previous.Used {
					boundary = "early reset observed; cause unknown"
				}
			}
			r.Cycles = append(r.Cycles, Cycle{Start: m.At, End: m.At, Reset: m.Reset, Quota: m.Quota, Plan: m.Plan, StartUsed: m.Used, EndUsed: m.Used, Boundary: boundary, Points: []Point{{At: m.At, Used: m.Used}}})
			cycle = &r.Cycles[len(r.Cycles)-1]
			anchor = m
		} else if m.At.After(previous.At) && cycle.EndUsed < 100 {
			delta := m.Used - anchor.Used
			lastInWindow := index == len(meters)-1 || meters[index+1].Quota != m.Quota || meters[index+1].Plan != m.Plan || meters[index+1].Used < m.Used-5 || (!m.Reset.IsZero() && !meters[index+1].Reset.IsZero() && math.Abs(meters[index+1].Reset.Sub(m.Reset).Seconds()) > 60)
			if delta >= 5 || m.Used >= 100 || (delta > 0 && lastInWindow) {
				from := sort.Search(len(events), func(i int) bool { return events[i].At.After(anchor.At) })
				cost := 0.0
				unknown := 0
				only := ""
				mixed := false
				outputs := 0
				count := 0
				// An hourly row can start before this interval and still overlap it.
				// Its total cannot establish where within the hour quota was spent.
				if archiveEnds[from].After(anchor.At) {
					cycle.ArchiveTiming = true
					mixed = true
				}
				for j := from; j < len(events) && !events[j].At.After(m.At); j++ {
					e := events[j]
					if e.Plan != "" && m.Plan != "" && e.Plan != m.Plan {
						continue
					}
					if !matchesQuota(e, m.Quota) {
						continue
					}
					if !e.End.IsZero() && e.End.After(m.At) {
						unknown += e.Requests
						continue
					}
					cost += e.Cost
					count += e.Requests
					outputs += e.Output
					if !e.Priced {
						unknown += e.Requests
					}
					k := modelKey(e)
					if only == "" {
						only = k
					} else if only != k {
						mixed = true
					}
					if e.Archived {
						cycle.ArchiveTiming = true
						mixed = true
					}
				}
				if count == 0 {
					cycle.UnmatchedPoints += delta
				}
				cycle.Cost += cost
				cycle.UnknownRequests += unknown
				cycle.End = m.At
				cycle.EndUsed = m.Used
				cycle.Used = m.Used - cycle.StartUsed
				cycle.Points = append(cycle.Points, Point{At: m.At, Used: m.Used, Cost: cycle.Cost})
				if (m.Quota == "seven_day" || m.Quota == "weekly" || strings.HasSuffix(m.Quota, ":weekly")) && delta >= 5 && unknown == 0 && count > 0 && !mixed {
					base := models[only]
					idx := -1
					for i, v := range cycle.Models {
						if v.Model == base.Model && v.Effort == base.Effort && v.Speed == base.Speed {
							idx = i
							break
						}
					}
					if idx < 0 {
						cycle.Models = append(cycle.Models, Model{Model: base.Model, Effort: base.Effort, Speed: base.Speed})
						idx = len(cycle.Models) - 1
					}
					v := &cycle.Models[idx]
					v.QuotaPoints += delta
					v.MatchedCost += cost
					v.MatchedOutput += outputs
				}
				anchor = m
			}
		}
		previous = m
	}
	for i := range r.Cycles {
		c := &r.Cycles[i]
		for j := range c.Models {
			m := &c.Models[j]
			if m.QuotaPoints >= 5 {
				m.Per100 = number(m.MatchedCost * 100 / m.QuotaPoints)
				m.OutputPerPoint = number(float64(m.MatchedOutput) / m.QuotaPoints)
			}
		}
		c.Complete = c.StartUsed <= 1 && c.EndUsed >= 99 && c.UnknownRequests == 0 && c.UnmatchedPoints == 0 && !c.ArchiveTiming && c.Cost > 0
		if c.Used >= 5 && c.Cost > 0 && c.UnknownRequests == 0 && c.UnmatchedPoints == 0 && !c.ArchiveTiming {
			c.Per100 = number(c.Cost * 100 / c.Used)
			if profile.Multiplier > 0 && (profile.PlanType == "" || c.Plan == "" || c.Plan == profile.PlanType) {
				c.X1 = number(*c.Per100 / profile.Multiplier)
			}
			c.Low = number(c.Cost * 100 / math.Min(100, c.Used+1))
			c.High = number(c.Cost * 100 / math.Max(1, c.Used-1))
		}
	}
	for _, m := range models {
		if m.QuotaPoints >= 5 {
			m.Per100 = number(m.MatchedCost * 100 / m.QuotaPoints)
			m.OutputPerPoint = number(float64(m.MatchedOutput) / m.QuotaPoints)
		}
		r.Models = append(r.Models, *m)
	}
	sort.Slice(r.Models, func(i, j int) bool { return r.Models[i].Cost > r.Models[j].Cost })
	for _, d := range devices {
		r.Devices = append(r.Devices, *d)
	}
	sort.Slice(r.Devices, func(i, j int) bool { return r.Devices[i].Cost > r.Devices[j].Cost })
	if r.UnknownRequests > 0 {
		r.Warnings = append(r.Warnings, "Some requests have no known price. Dollar totals and subscription multiples are lower bounds.")
	}
	if r.ArchivedRequests > 0 {
		r.Warnings = append(r.Warnings, "Hourly archives have coarser timing and no per-request pricing details; excluded from model quota calibration.")
	}
	if r.RejectedMeterCorrections > 0 {
		r.Warnings = append(r.Warnings, "Small backwards meter observations (up to 5pp with unchanged reset) were rejected as concurrent stale counters or corrections. They do not add or subtract allowance.")
	}
	r.Warnings = append(r.Warnings, "API-equivalent value is not an invoice or a measure of task quality. Shared web, cloud and other-device activity may consume quota without local token records.", "x1 and other-plan projections assume linear scaling. They are scenarios, not measured entitlements on other plans. Rounding bounds cover meter precision only.")
	return r
}

// Polls and task logs can lag each other. Prefer the direct rollout counter
// within ten minutes rather than creating fake resets from the stale poll.
func preferRolloutMeters(meters []Meter) []Meter {
	direct := map[string][]time.Time{}
	for _, m := range meters {
		if m.Source == "rollout" {
			k := m.Quota + "|" + m.Plan
			direct[k] = append(direct[k], m.At)
		}
	}
	for k := range direct {
		sort.Slice(direct[k], func(i, j int) bool { return direct[k][i].Before(direct[k][j]) })
	}
	out := make([]Meter, 0, len(meters))
	for _, m := range meters {
		if m.Source == "poll" {
			times := direct[m.Quota+"|"+m.Plan]
			i := sort.Search(len(times), func(i int) bool { return !times[i].Before(m.At.Add(-10 * time.Minute)) })
			if i < len(times) && !times[i].After(m.At.Add(10*time.Minute)) {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}
