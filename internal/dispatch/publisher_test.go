package dispatch

import (
	"context"
	"path/filepath"
	"testing"

	"heatpulse/internal/audit"
	"heatpulse/internal/station"
	"heatpulse/internal/store"
)

func newPublisherFixture(t *testing.T) (*Publisher, *Planner, *station.TargetService, *station.Registry, *audit.Query) {
	t.Helper()
	registry := station.NewRegistry([]station.Station{
		{ID: "alpha", Name: "甲站", TargetCelsius: 68, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]station.ExchangerPhase{}},
		{ID: "bravo", Name: "乙站", TargetCelsius: 66, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]station.ExchangerPhase{}},
	})
	events := store.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"))
	recorder := audit.NewRecorder(events)
	targets := station.NewTargetService(registry, recorder)
	planner := NewPlanner(targets)
	return NewPublisher(planner, targets, recorder), planner, targets, registry, audit.NewQuery(events)
}

func beginPlan(t *testing.T, planner *Planner, requested map[string]float64) Plan {
	t.Helper()
	plan, err := planner.Begin(context.Background(), requested, 0)
	if err != nil {
		t.Fatalf("Begin plan: %v", err)
	}
	return plan
}

func TestPublishPartialWhenStationHeldThenReconcilesOnRelease(t *testing.T) {
	publisher, planner, targets, registry, query := newPublisherFixture(t)

	if _, err := targets.ManualHold(station.HoldRequest{
		StationIDs: []string{"alpha"}, Actor: "dispatcher-li", Reason: "抢修",
	}); err != nil {
		t.Fatal(err)
	}
	plan := beginPlan(t, planner, map[string]float64{"alpha": 72, "bravo": 70})

	published, err := publisher.Publish(plan.ID, "dispatcher-wang")
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if published.Status != PlanPartial {
		t.Fatalf("plan status = %q, want partial", published.Status)
	}
	alpha, _ := registry.Get("alpha")
	if alpha.TargetCelsius != 68 {
		t.Errorf("held target overwritten: %v", alpha.TargetCelsius)
	}
	bravo, _ := registry.Get("bravo")
	if bravo.TargetCelsius != 70 {
		t.Errorf("non-held target = %v, want 70", bravo.TargetCelsius)
	}

	blocked, err := query.Recent(audit.Filter{Action: "target.publish.blocked"})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Actor != "dispatcher-wang" {
		t.Fatalf("blocked audit = %+v", blocked)
	}

	result, err := targets.ReleaseHold("alpha", "chief", "恢复调度")
	if err != nil {
		t.Fatalf("ReleaseHold: %v", err)
	}
	if result.Revalidation != station.RevalidationApplied || result.Celsius != 72 {
		t.Fatalf("release result = %+v", result)
	}
	alpha, _ = registry.Get("alpha")
	if alpha.TargetCelsius != 72 {
		t.Errorf("target after release = %v, want 72", alpha.TargetCelsius)
	}
}

func TestPublishFullWhenNothingHeld(t *testing.T) {
	publisher, planner, _, registry, _ := newPublisherFixture(t)
	plan := beginPlan(t, planner, map[string]float64{"alpha": 71, "bravo": 69})

	published, err := publisher.Publish(plan.ID, "dispatcher-wang")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.Status != PlanPublished {
		t.Fatalf("plan status = %q, want published", published.Status)
	}
	if alpha, _ := registry.Get("alpha"); alpha.TargetCelsius != 71 {
		t.Errorf("alpha target = %v, want 71", alpha.TargetCelsius)
	}
}

func TestPublishMarksPlanStaleAfterHoldBumpsRevision(t *testing.T) {
	publisher, planner, targets, _, _ := newPublisherFixture(t)
	plan := beginPlan(t, planner, map[string]float64{"alpha": 71})
	// A batch hold after the plan began advances the target revision, so the
	// plan's captured source revision is now stale.
	if _, err := targets.ManualHold(station.HoldRequest{
		StationIDs: []string{"bravo"}, Actor: "li", Reason: "临时保持",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(plan.ID, "dispatcher-wang"); err == nil {
		t.Fatal("expected stale publish error")
	}
	updated, ok := planner.Plan(plan.ID)
	if !ok || updated.Status != PlanStale {
		t.Fatalf("plan status = %+v, want stale", updated)
	}
}

func TestPublishRefusesNonCalculatingPlan(t *testing.T) {
	publisher, planner, _, _, _ := newPublisherFixture(t)
	plan := beginPlan(t, planner, map[string]float64{"alpha": 71})
	if _, err := publisher.Publish(plan.ID, "wang"); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if _, err := publisher.Publish(plan.ID, "wang"); err == nil {
		t.Fatal("expected error republishing a published plan")
	}
}
