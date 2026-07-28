package notify

import (
	"strings"
	"time"
)

const (
	weeklyPaceWindow           = 7 * 24 * time.Hour
	paceOverHardThreshold      = 10.0
	paceVeryOverHardThreshold  = 15.0
	paceUnderHardThreshold     = 10.0
	paceVeryUnderHardThreshold = 15.0
	paceTimeSliceFraction      = 0.20
	paceVeryTimeSliceFraction  = 0.30
	paceMinimumDelta           = 1.0
)

type PaceTier string

const (
	PaceOn        PaceTier = "on"
	PaceOver      PaceTier = "over"
	PaceVeryOver  PaceTier = "very_over"
	PaceUnder     PaceTier = "under"
	PaceVeryUnder PaceTier = "very_under"
)

type WeeklyPace struct {
	Tier                   PaceTier
	Trigger                string
	ExpectedUsed           float64
	Delta                  float64
	OverTimeThreshold      float64
	VeryOverTimeThreshold  float64
	UnderTimeThreshold     float64
	VeryUnderTimeThreshold float64
	TimeLeftOverPercent    float64
	ElapsedUnderPercent    float64
}

func EvaluateWeeklyPace(quotaName string, utilization float64, resetsAt, now time.Time) (WeeklyPace, bool) {
	if !isWeeklyPaceQuota(quotaName) {
		return WeeklyPace{}, false
	}

	timeLeft := resetsAt.Sub(now)
	if timeLeft < 0 {
		timeLeft = 0
	}
	if timeLeft > weeklyPaceWindow {
		timeLeft = weeklyPaceWindow
	}
	expectedUsed := 100 - (float64(timeLeft) / float64(weeklyPaceWindow) * 100)
	expectedUsed = clampPercent(expectedUsed)
	delta := utilization - expectedUsed
	remainingExpected := 100 - expectedUsed
	result := WeeklyPace{
		Tier:                   PaceOn,
		ExpectedUsed:           expectedUsed,
		Delta:                  delta,
		OverTimeThreshold:      remainingExpected * paceTimeSliceFraction,
		VeryOverTimeThreshold:  remainingExpected * paceVeryTimeSliceFraction,
		UnderTimeThreshold:     expectedUsed * paceTimeSliceFraction,
		VeryUnderTimeThreshold: expectedUsed * paceVeryTimeSliceFraction,
		TimeLeftOverPercent:    timeLeftOverPercent(delta, remainingExpected),
		ElapsedUnderPercent:    elapsedUnderPercent(delta, expectedUsed),
	}
	if delta > -paceMinimumDelta && delta < paceMinimumDelta {
		return result, true
	}
	if tier, trigger := overPaceTier(result); tier != PaceOn {
		result.Tier = tier
		result.Trigger = trigger
		return result, true
	}
	result.Tier, result.Trigger = underPaceTier(result)
	return result, true
}

func isWeeklyPaceQuota(quotaName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(quotaName))
	switch normalized {
	case "seven_day", "seven_day_sonnet":
		return true
	default:
		return strings.Contains(normalized, "weekly") || strings.HasPrefix(normalized, "wkly_")
	}
}

func overPaceTier(p WeeklyPace) (PaceTier, string) {
	if p.Delta <= 0 {
		return PaceOn, ""
	}
	switch {
	case p.Delta >= paceVeryOverHardThreshold:
		return PaceVeryOver, "flat"
	case p.Delta >= p.VeryOverTimeThreshold:
		return PaceVeryOver, "time left"
	case p.Delta >= paceOverHardThreshold:
		return PaceOver, "flat"
	case p.Delta >= p.OverTimeThreshold:
		return PaceOver, "time left"
	default:
		return PaceOn, ""
	}
}

func underPaceTier(p WeeklyPace) (PaceTier, string) {
	if p.Delta >= 0 {
		return PaceOn, ""
	}
	reserve := -p.Delta
	switch {
	case reserve >= paceVeryUnderHardThreshold:
		return PaceVeryUnder, "flat"
	case reserve >= p.VeryUnderTimeThreshold:
		return PaceVeryUnder, "elapsed"
	case reserve >= paceUnderHardThreshold:
		return PaceUnder, "flat"
	case reserve >= p.UnderTimeThreshold:
		return PaceUnder, "elapsed"
	default:
		return PaceOn, ""
	}
}

func timeLeftOverPercent(delta, remainingExpected float64) float64 {
	if delta <= 0 {
		return 0
	}
	if remainingExpected <= 0 {
		return 100
	}
	return (delta / remainingExpected) * 100
}

func elapsedUnderPercent(delta, expectedUsed float64) float64 {
	if delta >= 0 {
		return 0
	}
	if expectedUsed <= 0 {
		return 100
	}
	return (-delta / expectedUsed) * 100
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
