package station

import (
	"path/filepath"
	"testing"

	"heatpulse/internal/audit"
	"heatpulse/internal/store"
)

func newTestService(t *testing.T) (*TargetService, *Registry, *audit.Query) {
	t.Helper()
	registry := NewRegistry([]Station{
		{ID: "alpha", Name: "甲站", TargetCelsius: 68, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]ExchangerPhase{}},
		{ID: "bravo", Name: "乙站", TargetCelsius: 66, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]ExchangerPhase{}},
	})
	events := store.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"))
	recorder := audit.NewRecorder(events)
	return NewTargetService(registry, recorder), registry, audit.NewQuery(events)
}

func TestManualHoldBatchCarriesActorReasonAndVersion(t *testing.T) {
	svc, registry, query := newTestService(t)

	batch, err := svc.ManualHold(HoldRequest{
		StationIDs: []string{"alpha", "bravo"}, Actor: "dispatcher-li", Reason: "管网抢修",
	})
	if err != nil {
		t.Fatalf("ManualHold returned error: %v", err)
	}
	if len(batch.Holds) != 2 {
		t.Fatalf("expected 2 holds, got %d", len(batch.Holds))
	}
	versions := map[string]uint64{}
	for _, hold := range batch.Holds {
		if hold.Actor != "dispatcher-li" || hold.Reason != "管网抢修" {
			t.Errorf("hold for %s missing actor/reason: %+v", hold.StationID, hold)
		}
		if hold.Version == 0 || hold.Revision == 0 {
			t.Errorf("hold for %s has zero version/revision: %+v", hold.StationID, hold)
		}
		versions[hold.StationID] = hold.Version
	}
	for _, id := range []string{"alpha", "bravo"} {
		item, _ := registry.Get(id)
		if !item.Hold || item.HoldInfo == nil {
			t.Fatalf("station %s not held with info", id)
		}
		if item.HoldInfo.Actor != "dispatcher-li" || item.HoldInfo.Reason != "管网抢修" {
			t.Errorf("station %s hold info = %+v", id, item.HoldInfo)
		}
		if item.TargetCelsius != 68 && id == "alpha" {
			t.Errorf("hold changed target: %v", item.TargetCelsius)
		}
	}

	entries, err := query.Recent(audit.Filter{Action: "target.hold"})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 hold audit entries, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Actor != "dispatcher-li" {
			t.Errorf("audit actor = %q", entry.Actor)
		}
		if entry.Details["reason"] != "管网抢修" {
			t.Errorf("audit reason detail = %v", entry.Details["reason"])
		}
		// Audit entries are persisted as JSON, so numbers decode back as float64.
		if version, ok := entry.Details["version"].(float64); !ok || uint64(version) != versions[entry.EntityID] {
			t.Errorf("audit version for %s = %v, want %d", entry.EntityID, entry.Details["version"], versions[entry.EntityID])
		}
	}
}

func TestManualHoldRequiresActorReasonAndStations(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "a", Reason: ""}); err == nil {
		t.Error("expected error when reason missing")
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "", Reason: "r"}); err == nil {
		t.Error("expected error when actor missing")
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: nil, Actor: "a", Reason: "r"}); err == nil {
		t.Error("expected error with no stations")
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha", "alpha"}, Actor: "a", Reason: "r"}); err == nil {
		t.Error("expected error on duplicate stations")
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"ghost"}, Actor: "a", Reason: "r"}); err == nil {
		t.Error("expected error for unknown station")
	}
}

func TestRepeatedHoldBumpsVersion(t *testing.T) {
	svc, registry, _ := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "第一次"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "wang", Reason: "第二次"}); err != nil {
		t.Fatal(err)
	}
	item, _ := registry.Get("alpha")
	if item.HoldInfo.Version != 2 {
		t.Fatalf("hold version = %d, want 2", item.HoldInfo.Version)
	}
	if item.HoldInfo.Actor != "wang" || item.HoldInfo.Reason != "第二次" {
		t.Errorf("hold info not refreshed: %+v", item.HoldInfo)
	}

	result, err := svc.ReleaseHold("alpha", "chief", "抢修结束")
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 2 {
		t.Errorf("release reports version %d, want 2", result.Version)
	}
}

func TestPublishProtectsHeldStations(t *testing.T) {
	svc, registry, query := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "抢修"}); err != nil {
		t.Fatal(err)
	}
	baseRevision := svc.Revision()

	report, err := svc.CompareAndPublish("plan-1", "dispatcher-wang", baseRevision, map[string]float64{"alpha": 72, "bravo": 70})
	if err != nil {
		t.Fatalf("CompareAndPublish error: %v", err)
	}
	if len(report.Applied) != 1 || report.Applied[0] != "bravo" {
		t.Errorf("applied = %v, want [bravo]", report.Applied)
	}
	if len(report.Blocked) != 1 {
		t.Fatalf("blocked = %v, want one entry", report.Blocked)
	}
	blocked := report.Blocked[0]
	if blocked.StationID != "alpha" || blocked.Actor != "li" || blocked.Reason != "抢修" || blocked.Celsius != 72 {
		t.Errorf("blocked entry = %+v", blocked)
	}
	alpha, _ := registry.Get("alpha")
	if alpha.TargetCelsius != 68 {
		t.Errorf("held station target overwritten: %v", alpha.TargetCelsius)
	}
	bravo, _ := registry.Get("bravo")
	if bravo.TargetCelsius != 70 {
		t.Errorf("non-held station target = %v, want 70", bravo.TargetCelsius)
	}

	entries, err := query.Recent(audit.Filter{Action: "target.publish.blocked"})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 blocked audit entry, got %d", len(entries))
	}
	if entries[0].Outcome != audit.OutcomeRejected || entries[0].Details["held_by"] != "li" {
		t.Errorf("blocked audit entry = %+v", entries[0])
	}
}

func TestReleaseRevalidatesAgainstNewestPlan(t *testing.T) {
	svc, registry, query := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "抢修"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompareAndPublish("plan-9", "dispatcher-wang", svc.Revision(), map[string]float64{"alpha": 73, "bravo": 70}); err != nil {
		t.Fatal(err)
	}
	alpha, _ := registry.Get("alpha")
	if alpha.TargetCelsius != 68 {
		t.Fatalf("setup: held target changed to %v", alpha.TargetCelsius)
	}

	result, err := svc.ReleaseHold("alpha", "chief", "恢复调度")
	if err != nil {
		t.Fatalf("ReleaseHold error: %v", err)
	}
	if result.Revalidation != RevalidationApplied || result.Celsius != 73 {
		t.Fatalf("release result = %+v, want applied 73", result)
	}
	if result.PlanID != "plan-9" {
		t.Errorf("plan id = %q, want plan-9", result.PlanID)
	}
	alpha, _ = registry.Get("alpha")
	if alpha.TargetCelsius != 73 {
		t.Errorf("target after release = %v, want 73", alpha.TargetCelsius)
	}
	if alpha.Hold || alpha.HoldInfo != nil {
		t.Errorf("hold metadata not cleared: %+v", alpha.HoldInfo)
	}

	entries, err := query.Recent(audit.Filter{Action: "target.revalidate"})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 1 || entries[0].Outcome != audit.OutcomeAccepted {
		t.Fatalf("revalidate audit entries = %+v", entries)
	}
	releaseEntries, _ := query.Recent(audit.Filter{Action: "target.hold.release"})
	if len(releaseEntries) != 1 || releaseEntries[0].Actor != "chief" {
		t.Fatalf("release audit entries = %+v", releaseEntries)
	}
}

func TestReleaseWithoutNewerPlanLeavesTargetUnchanged(t *testing.T) {
	svc, registry, _ := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "抢修"}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ReleaseHold("alpha", "chief", "恢复")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revalidation != RevalidationUnchanged {
		t.Errorf("revalidation = %q, want unchanged", result.Revalidation)
	}
	alpha, _ := registry.Get("alpha")
	if alpha.TargetCelsius != 68 {
		t.Errorf("target changed to %v", alpha.TargetCelsius)
	}
}

func TestReleaseRejectsPlanTargetOutsideEnvelope(t *testing.T) {
	svc, registry, query := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "抢修"}); err != nil {
		t.Fatal(err)
	}
	// Envelope is not enforced for held stations; the requested value is
	// remembered and re-checked when the hold lifts.
	if _, err := svc.CompareAndPublish("plan-bad", "dispatcher-wang", svc.Revision(), map[string]float64{"alpha": 99}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ReleaseHold("alpha", "chief", "恢复")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revalidation != RevalidationRejected {
		t.Fatalf("revalidation = %q, want rejected", result.Revalidation)
	}
	alpha, _ := registry.Get("alpha")
	if alpha.TargetCelsius != 68 {
		t.Errorf("invalid target applied on release: %v", alpha.TargetCelsius)
	}
	entries, _ := query.Recent(audit.Filter{Action: "target.revalidate"})
	if len(entries) != 1 || entries[0].Outcome != audit.OutcomeRejected {
		t.Fatalf("revalidate audit entries = %+v", entries)
	}
}

func TestReleaseUsesNewestPlanForStationEvenWhenLaterPlansOmitIt(t *testing.T) {
	svc, registry, _ := newTestService(t)
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "抢修"}); err != nil {
		t.Fatal(err)
	}
	// Plan targeting alpha is blocked while alpha is held.
	if _, err := svc.CompareAndPublish("plan-a1", "wang", svc.Revision(), map[string]float64{"alpha": 74}); err != nil {
		t.Fatal(err)
	}
	// A newer plan only touches bravo; alpha must keep its own newest intent.
	if _, err := svc.CompareAndPublish("plan-b1", "wang", svc.Revision(), map[string]float64{"bravo": 69}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ReleaseHold("alpha", "chief", "恢复")
	if err != nil {
		t.Fatal(err)
	}
	if result.PlanID != "plan-a1" || result.Celsius != 74 || result.Revalidation != RevalidationApplied {
		t.Fatalf("release result = %+v", result)
	}
	if alpha, _ := registry.Get("alpha"); alpha.TargetCelsius != 74 {
		t.Errorf("alpha target = %v, want 74", alpha.TargetCelsius)
	}
}

func TestReleaseRequiresActorReasonAndActiveHold(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.ReleaseHold("alpha", "", "reason"); err == nil {
		t.Error("expected error when actor missing")
	}
	if _, err := svc.ReleaseHold("alpha", "chief", ""); err == nil {
		t.Error("expected error when reason missing")
	}
	if _, err := svc.ReleaseHold("alpha", "chief", "reason"); err == nil {
		t.Error("expected error when station is not held")
	}
}

func TestMaintenanceHoldIsProtectedFromManualOperations(t *testing.T) {
	registry := NewRegistry([]Station{
		{ID: "alpha", TargetCelsius: 68, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]ExchangerPhase{}},
	})
	events := store.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"))
	svc := NewTargetService(registry, audit.NewRecorder(events))
	maintenance := NewMaintenanceService(registry, store.NewStationSnapshotStore(t.TempDir()))

	if _, err := maintenance.Enter("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "x"}); err == nil {
		t.Error("manual hold must be rejected during maintenance")
	}
	if _, err := svc.ReleaseHold("alpha", "li", "x"); err == nil {
		t.Error("manual release must be rejected during maintenance")
	}
	if _, err := maintenance.Exit("alpha"); err != nil {
		t.Fatal(err)
	}
	item, _ := registry.Get("alpha")
	if item.Hold || item.HoldInfo != nil {
		t.Errorf("maintenance hold metadata leaked: %+v", item.HoldInfo)
	}
}

func TestMaintenanceRestoresPreviousManualHold(t *testing.T) {
	registry := NewRegistry([]Station{
		{ID: "alpha", TargetCelsius: 68, TargetRevision: 1, ControllerEpoch: 1, Exchangers: map[string]ExchangerPhase{}},
	})
	events := store.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"))
	svc := NewTargetService(registry, audit.NewRecorder(events))
	maintenance := NewMaintenanceService(registry, store.NewStationSnapshotStore(t.TempDir()))

	if _, err := svc.ManualHold(HoldRequest{StationIDs: []string{"alpha"}, Actor: "li", Reason: "既有保持"}); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Enter("alpha"); err != nil {
		t.Fatal(err)
	}
	item, _ := registry.Get("alpha")
	if item.HoldInfo == nil || item.HoldInfo.Actor != "maintenance" {
		t.Fatalf("maintenance hold info = %+v", item.HoldInfo)
	}
	if _, err := maintenance.Exit("alpha"); err != nil {
		t.Fatal(err)
	}
	item, _ = registry.Get("alpha")
	if !item.Hold || item.HoldInfo == nil || item.HoldInfo.Actor != "li" || item.HoldInfo.Reason != "既有保持" {
		t.Fatalf("previous manual hold not restored: %+v", item.HoldInfo)
	}
}

func TestStalePublishRejected(t *testing.T) {
	svc, _, _ := newTestService(t)
	if _, err := svc.CompareAndPublish("plan-1", "dispatcher-wang", svc.Revision(), map[string]float64{"alpha": 70}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompareAndPublish("plan-1", "dispatcher-wang", 1, map[string]float64{"alpha": 71}); err == nil {
		t.Error("expected stale revision error")
	}
}
