package subscription

import (
	"testing"
	"time"
)

func TestMeasuredValueAndResetIsolation(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	meters := []Meter{{At: at, Used: 0, Quota: "seven_day", Reset: at.Add(7 * 24 * time.Hour)}, {At: at.Add(time.Hour), Used: 50, Quota: "seven_day", Reset: at.Add(7 * 24 * time.Hour)}, {At: at.Add(2 * time.Hour), Used: 100, Quota: "seven_day", Reset: at.Add(7 * 24 * time.Hour)}, {At: at.Add(3 * time.Hour), Used: 0, Quota: "seven_day", Reset: at.Add(8 * 24 * time.Hour)}}
	events := []Event{{At: at.Add(30 * time.Minute), Model: "m", Cost: 500, Priced: true, Requests: 1}, {At: at.Add(90 * time.Minute), Model: "m", Cost: 500, Priced: true, Requests: 1}, {At: at.Add(150 * time.Minute), Model: "m", Cost: 99, Priced: true, Requests: 1}}
	r := Analyze(events, meters, Profile{MonthlyUSD: 200, Multiplier: 20})
	if len(r.Cycles) != 2 || r.Cycles[0].Cost != 1000 || *r.Cycles[0].Per100 != 1000 || *r.Cycles[0].X1 != 50 {
		t.Fatalf("cycles: %+v", r.Cycles)
	}
	if *r.ValueMultiple != 5.495 {
		t.Fatalf("monthly value %v", *r.ValueMultiple)
	}
	if !r.Cycles[0].Complete {
		t.Fatal("0 to 100 should be measured complete")
	}
}
func TestPartialUnknownAndMixedModels(t *testing.T) {
	at := time.Now().UTC()
	end := at.Add(time.Hour)
	meters := []Meter{{At: at, Used: 38, Quota: "seven_day"}, {At: end, Used: 100, Quota: "seven_day"}}
	r := Analyze([]Event{{At: at.Add(time.Minute), Model: "a", Cost: 62, Priced: true, Requests: 1}, {At: at.Add(2 * time.Minute), Model: "b", Requests: 1}}, meters, Profile{Multiplier: 20})
	c := r.Cycles[0]
	if c.Complete || c.Per100 != nil || c.Used != 62 {
		t.Fatalf("partial/unpriced %+v", c)
	}
	for _, m := range r.Models {
		if m.QuotaPoints != 0 {
			t.Fatal("mixed model quota must not be allocated by token cost")
		}
	}
}
func TestResetJitterAndFlatMeter(t *testing.T) {
	at := time.Now().UTC()
	reset := at.Add(7 * 24 * time.Hour)
	r := Analyze([]Event{{At: at.Add(time.Minute), Model: "a", Cost: 10, Priced: true, Requests: 1}}, []Meter{{At: at, Quota: "seven_day", Reset: reset}, {At: at.Add(time.Hour), Quota: "seven_day", Reset: reset.Add(time.Second)}}, Profile{})
	if len(r.Cycles) != 1 || r.Cycles[0].Per100 != nil {
		t.Fatal("flat rounded meter or reset timestamp jitter must not create an allowance estimate")
	}
}

func TestConcurrentRoundedCountersDoNotCreateFakeResets(t *testing.T) {
	at := time.Now().UTC()
	reset := at.Add(7 * 24 * time.Hour)
	meters := []Meter{}
	for i, u := range []float64{0, 20, 19, 40, 39, 60, 59, 80, 79, 100, 99, 100} {
		meters = append(meters, Meter{At: at.Add(time.Duration(i) * time.Minute), Reset: reset, Quota: "seven_day", Used: u})
	}
	r := Analyze([]Event{{At: at.Add(time.Second), Model: "m", Cost: 20, Priced: true, Requests: 1}, {At: at.Add(2*time.Minute + time.Second), Model: "m", Cost: 20, Priced: true, Requests: 1}, {At: at.Add(4*time.Minute + time.Second), Model: "m", Cost: 20, Priced: true, Requests: 1}, {At: at.Add(6*time.Minute + time.Second), Model: "m", Cost: 20, Priced: true, Requests: 1}, {At: at.Add(8*time.Minute + time.Second), Model: "m", Cost: 20, Priced: true, Requests: 1}}, meters, Profile{})
	if len(r.Cycles) != 1 || !r.Cycles[0].Complete || *r.Cycles[0].Per100 != 100 {
		t.Fatalf("rounded meter reversals fragmented an allowance: %+v", r.Cycles)
	}
}

func TestHistoricalPlanDoesNotUseCurrentMultiplier(t *testing.T) {
	at := time.Now().UTC()
	r := Analyze([]Event{{At: at.Add(time.Minute), Cost: 100, Priced: true, Requests: 1}}, []Meter{{At: at, Quota: "seven_day", Plan: "prolite"}, {At: at.Add(time.Hour), Quota: "seven_day", Plan: "prolite", Used: 100}}, Profile{Multiplier: 20, PlanType: "pro"})
	if r.Cycles[0].Per100 == nil || r.Cycles[0].X1 != nil {
		t.Fatal("historical x5 quota must not use current x20 multiplier")
	}
}
