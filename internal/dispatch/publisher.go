package dispatch

import (
	"fmt"
	"time"

	"heatpulse/internal/audit"
	"heatpulse/internal/station"
)

type Publisher struct {
	planner *Planner
	targets *station.TargetService
	audit   *audit.Recorder
}

func NewPublisher(planner *Planner, targets *station.TargetService, recorder *audit.Recorder) *Publisher {
	return &Publisher{planner: planner, targets: targets, audit: recorder}
}

func (p *Publisher) Publish(planID, actor string) (Plan, error) {
	plan, ok := p.planner.Plan(planID)
	if !ok {
		return Plan{}, fmt.Errorf("plan %s not found", planID)
	}
	if plan.Status != PlanCalculating {
		return plan, fmt.Errorf("plan %s is %s", plan.ID, plan.Status)
	}
	report, err := p.targets.CompareAndPublish(plan.ID, actor, plan.SourceRevision, plan.Targets)
	if err != nil {
		plan.Status = PlanStale
		plan.Message = err.Error()
		p.planner.save(plan)
		_ = p.audit.Rejected("dispatch.publish", plan.ID, actor, err.Error(), map[string]any{"source_revision": plan.SourceRevision})
		return plan, err
	}
	now := time.Now().UTC()
	plan.PublishedAt = now
	if len(report.Blocked) > 0 {
		// Held stations were protected; the plan is only partially published.
		plan.Status = PlanPartial
		plan.Message = fmt.Sprintf("%d station target(s) protected by manual hold", len(report.Blocked))
	} else {
		plan.Status = PlanPublished
	}
	p.planner.save(plan)

	if err := p.audit.Accepted("dispatch.publish", plan.ID, actor, "dispatch targets published", map[string]any{
		"source_revision": plan.SourceRevision,
		"applied":         report.Applied,
		"blocked":         blockedStationIDs(report.Blocked),
	}); err != nil {
		return plan, err
	}
	return plan, nil
}

func blockedStationIDs(blocked []station.BlockedTarget) []string {
	ids := make([]string, 0, len(blocked))
	for _, item := range blocked {
		ids = append(ids, item.StationID)
	}
	return ids
}
