package subscription

import "time"

type WeeklyValuePoint struct {
	At            time.Time `json:"at"`
	ObservedUSD   float64   `json:"observedUsd"`
	WithoutRefill float64   `json:"withoutRefillUsd"`
}

type WeeklyValue struct {
	Quota            string             `json:"quota"`
	EarlyRefills     int                `json:"earlyRefills"`
	SkippedIntervals int                `json:"skippedIntervals"`
	Points           []WeeklyValuePoint `json:"points"`
}

// weeklyResetValue replays one primary weekly meter at a time. During an
// observed early refill it holds the original deadline and spends only the
// old quota's remaining percentage. Both traces resume regular quota at
// that deadline; missing intervals never supply a dollar estimate.
func weeklyResetValue(cycles []Cycle) []WeeklyValue {
	var result []WeeklyValue
	var value *WeeklyValue
	var plan, day string
	var deadline time.Time
	var remaining, observedUSD, withoutUSD float64
	for _, cycle := range cycles {
		switch cycle.Quota {
		case "seven_day", "weekly", "antigravity_gemini_pro:weekly":
		default:
			continue
		}
		if value == nil || cycle.Quota != value.Quota {
			result = append(result, WeeklyValue{Quota: cycle.Quota})
			value = &result[len(result)-1]
			observedUSD, withoutUSD, day = 0, 0, ""
			deadline = time.Time{}
		}
		if cycle.Plan != plan || !cycle.Start.Before(deadline) {
			deadline = time.Time{}
		}
		plan = cycle.Plan
		if cycle.earlyRefill {
			value.EarlyRefills++
			if deadline.IsZero() {
				deadline, remaining = cycle.priorReset, 100-cycle.priorUsed
			}
		}
		if !deadline.IsZero() {
			// The first reading after a boundary may already have spent quota.
			remaining = max(0, remaining-cycle.StartUsed)
		}
		for i := 1; i < len(cycle.Points); i++ {
			before, after := cycle.Points[i-1], cycle.Points[i]
			delta := after.Used - before.Used
			if delta <= 0 {
				continue
			}
			active := !deadline.IsZero() && before.At.Before(deadline)
			if active && after.At.After(deadline) {
				// An interval across the old deadline cannot be timed on
				// either side reliably. Exclude it from both curves.
				value.SkippedIntervals++
				deadline = time.Time{}
				continue
			}
			fraction := 1.0
			if active {
				fraction = min(remaining, delta) / delta
				remaining = max(0, remaining-delta)
			}
			if !after.matched {
				value.SkippedIntervals++
				continue
			}
			cost := after.Cost - before.Cost
			observedUSD += cost
			withoutUSD += cost * fraction
			point := WeeklyValuePoint{At: after.At, ObservedUSD: observedUSD, WithoutRefill: withoutUSD}
			currentDay := after.At.UTC().Format(time.DateOnly)
			if currentDay == day {
				value.Points[len(value.Points)-1] = point
			} else {
				value.Points = append(value.Points, point)
				day = currentDay
			}
		}
	}
	return result
}
