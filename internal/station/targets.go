package station

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"heatpulse/internal/audit"
)

// HoldRequest applies a manual hold to a batch of stations. Every station in
// the batch is held by the same operator for the same reason; each station
// still receives its own hold version.
type HoldRequest struct {
	StationIDs []string
	Actor      string
	Reason     string
}

// HoldResult reports the hold state of one station after a batch hold.
type HoldResult struct {
	StationID string    `json:"station_id"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason"`
	Version   uint64    `json:"version"`
	Revision  uint64    `json:"revision"`
	HeldAt    time.Time `json:"held_at"`
}

// HoldBatch is the outcome of a batch hold.
type HoldBatch struct {
	Revision uint64       `json:"revision"`
	Holds    []HoldResult `json:"holds"`
}

// ReleaseResult reports what happened after a hold was lifted and the station
// target was revalidated against the newest dispatch plan.
type ReleaseResult struct {
	StationID    string  `json:"station_id"`
	Actor        string  `json:"actor"`
	Reason       string  `json:"reason"`
	Version      uint64  `json:"version"`
	Revision     uint64  `json:"revision"`
	Revalidation string  `json:"revalidation"`
	Celsius      float64 `json:"celsius,omitempty"`
	PlanID       string  `json:"plan_id,omitempty"`
	PlanRevision uint64  `json:"plan_revision,omitempty"`
	Message      string  `json:"message,omitempty"`
}

// BlockedTarget names a held station whose target a dispatch publish was not
// allowed to overwrite.
type BlockedTarget struct {
	StationID string  `json:"station_id"`
	Actor     string  `json:"actor"`
	Reason    string  `json:"reason"`
	Version   uint64  `json:"version"`
	Celsius   float64 `json:"celsius"`
}

// PublishReport describes a publish after held stations were protected.
type PublishReport struct {
	Revision uint64          `json:"revision"`
	Applied  []string        `json:"applied"`
	Blocked  []BlockedTarget `json:"blocked"`
}

const (
	RevalidationApplied   = "applied"
	RevalidationUnchanged = "unchanged"
	RevalidationRejected  = "rejected"
)

type TargetService struct {
	registry *Registry
	audit    *audit.Recorder
	mu       sync.Mutex
	revision uint64
	// versions holds the per-station hold version; it only moves forward.
	versions map[string]uint64
	// pendingTargets remembers, per station, the newest dispatch plan that
	// requested a target for it (including requests blocked by a hold), so a
	// released hold can be revalidated against the dispatcher's latest
	// intent for that station even when newer plans target other stations.
	pendingTargets map[string]plannedTarget
}

type plannedTarget struct {
	PlanID   string
	Revision uint64
	Celsius  float64
}

func NewTargetService(registry *Registry, recorder *audit.Recorder) *TargetService {
	var revision uint64
	for _, item := range registry.List() {
		if item.TargetRevision > revision {
			revision = item.TargetRevision
		}
	}
	return &TargetService{
		registry: registry, audit: recorder, revision: revision,
		versions: make(map[string]uint64), pendingTargets: make(map[string]plannedTarget),
	}
}

func (s *TargetService) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

// ManualHold freezes the current target of every station in the batch. The
// operator and reason are mandatory and recorded per station together with a
// per-station hold version, so batch holds are fully auditable.
func (s *TargetService) ManualHold(request HoldRequest) (HoldBatch, error) {
	if request.Actor == "" || request.Reason == "" {
		return HoldBatch{}, fmt.Errorf("hold actor and reason are required")
	}
	if len(request.StationIDs) == 0 {
		return HoldBatch{}, fmt.Errorf("at least one station must be held")
	}
	seen := make(map[string]bool, len(request.StationIDs))
	for _, stationID := range request.StationIDs {
		if stationID == "" {
			return HoldBatch{}, fmt.Errorf("station id must not be empty")
		}
		if seen[stationID] {
			return HoldBatch{}, fmt.Errorf("station %s appears more than once in the hold batch", stationID)
		}
		seen[stationID] = true
		item, ok := s.registry.Get(stationID)
		if !ok {
			return HoldBatch{}, fmt.Errorf("station %s not found", stationID)
		}
		if item.Maintenance {
			return HoldBatch{}, fmt.Errorf("station %s is in maintenance; its hold is managed by the maintenance session", stationID)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	batch := HoldBatch{Revision: s.revision, Holds: make([]HoldResult, 0, len(request.StationIDs))}
	heldAt := time.Now().UTC()
	for _, stationID := range request.StationIDs {
		s.versions[stationID]++
		version := s.versions[stationID]
		info := HoldInfo{
			Actor: request.Actor, Reason: request.Reason, Version: version,
			Revision: s.revision, HeldAt: heldAt,
		}
		if _, err := s.registry.Update(stationID, func(item *Station) error {
			if item.Maintenance {
				return fmt.Errorf("station %s entered maintenance before the hold was applied", stationID)
			}
			item.Hold = true
			item.HoldInfo = &info
			item.TargetRevision = s.revision
			return nil
		}); err != nil {
			return HoldBatch{}, fmt.Errorf("hold %s by %s: %w", stationID, request.Actor, err)
		}
		// A new hold starts fresh: only plans blocked during this hold can
		// revalidate the target on release.
		delete(s.pendingTargets, stationID)
		batch.Holds = append(batch.Holds, HoldResult{
			StationID: stationID, Actor: request.Actor, Reason: request.Reason,
			Version: version, Revision: s.revision, HeldAt: heldAt,
		})
		if s.audit != nil {
			if err := s.audit.Accepted("target.hold", stationID, request.Actor,
				fmt.Sprintf("target held: %s", request.Reason), map[string]any{
					"reason": request.Reason, "version": version, "revision": s.revision,
					"batch_size": len(request.StationIDs),
				}); err != nil {
				return HoldBatch{}, err
			}
		}
	}
	return batch, nil
}

// ReleaseHold lifts the hold on a single station, records who released it and
// why, then revalidates the station target against the newest dispatch plan.
func (s *TargetService) ReleaseHold(stationID, actor, reason string) (ReleaseResult, error) {
	if actor == "" || reason == "" {
		return ReleaseResult{}, fmt.Errorf("release actor and reason are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.registry.Get(stationID)
	if !ok {
		return ReleaseResult{}, fmt.Errorf("station %s not found", stationID)
	}
	if !current.Hold {
		return ReleaseResult{}, fmt.Errorf("station %s is not held", stationID)
	}
	if current.Maintenance {
		return ReleaseResult{}, fmt.Errorf("station %s is in maintenance; exit maintenance to lift its hold", stationID)
	}
	version := uint64(0)
	if current.HoldInfo != nil {
		version = current.HoldInfo.Version
	}

	s.revision++
	result := ReleaseResult{
		StationID: stationID, Actor: actor, Reason: reason,
		Version: version, Revision: s.revision, Revalidation: RevalidationUnchanged,
	}
	if _, err := s.registry.Update(stationID, func(item *Station) error {
		if item.Maintenance {
			return fmt.Errorf("station %s entered maintenance before the hold was released", stationID)
		}
		item.Hold = false
		item.HoldInfo = nil
		item.TargetRevision = s.revision
		return nil
	}); err != nil {
		return ReleaseResult{}, err
	}
	if s.audit != nil {
		if err := s.audit.Accepted("target.hold.release", stationID, actor,
			fmt.Sprintf("hold released: %s", reason), map[string]any{
				"reason": reason, "hold_version": version, "revision": s.revision,
				"held_by": holdActor(current.HoldInfo),
			}); err != nil {
			return ReleaseResult{}, err
		}
	}

	pending, planned := s.pendingTargets[stationID]
	if planned {
		result.PlanID = pending.PlanID
		result.PlanRevision = pending.Revision
		result.Celsius = pending.Celsius
		if err := validateEnvelope(stationID, pending.Celsius); err != nil {
			result.Revalidation = RevalidationRejected
			result.Message = err.Error()
			if s.audit != nil {
				if auditErr := s.audit.Rejected("target.revalidate", stationID, actor, err.Error(), map[string]any{
					"plan_id": pending.PlanID, "plan_revision": pending.Revision,
					"celsius": pending.Celsius, "hold_version": version,
				}); auditErr != nil {
					return ReleaseResult{}, auditErr
				}
			}
			return result, nil
		}
		if _, err := s.registry.Update(stationID, func(item *Station) error {
			item.TargetCelsius = pending.Celsius
			item.TargetRevision = s.revision
			item.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			return ReleaseResult{}, err
		}
		delete(s.pendingTargets, stationID)
		result.Revalidation = RevalidationApplied
		if s.audit != nil {
			if err := s.audit.Accepted("target.revalidate", stationID, actor,
				"target revalidated against newest plan after release", map[string]any{
					"plan_id": pending.PlanID, "plan_revision": pending.Revision,
					"celsius": pending.Celsius, "revision": s.revision,
				}); err != nil {
				return ReleaseResult{}, err
			}
		}
	}
	return result, nil
}

// CompareAndPublish applies a dispatch plan. Held stations are never
// overwritten: they are reported as blocked while the other stations still
// receive their targets. Each blocked target is remembered per station as
// the dispatcher's newest intent, so a released hold can be revalidated
// against it even when later plans only target other stations.
func (s *TargetService) CompareAndPublish(planID, actor string, sourceRevision uint64, targets map[string]float64) (PublishReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sourceRevision != s.revision {
		return PublishReport{}, fmt.Errorf("dispatch source revision %d is stale; current is %d", sourceRevision, s.revision)
	}
	stationIDs := make([]string, 0, len(targets))
	for stationID := range targets {
		stationIDs = append(stationIDs, stationID)
	}
	sort.Strings(stationIDs)

	// Phase 1: validate the whole plan before mutating anything. Held
	// stations skip the envelope check here; their requested target is
	// re-checked when the hold is released.
	states := make(map[string]Station, len(stationIDs))
	for _, stationID := range stationIDs {
		item, ok := s.registry.Get(stationID)
		if !ok {
			return PublishReport{}, fmt.Errorf("station %s not found", stationID)
		}
		states[stationID] = item
		if !item.Hold {
			if err := validateEnvelope(stationID, targets[stationID]); err != nil {
				return PublishReport{}, err
			}
		}
	}

	// Phase 2: apply non-held targets and record pending ones for held stations.
	s.revision++
	report := PublishReport{Revision: s.revision, Applied: []string{}, Blocked: []BlockedTarget{}}
	for _, stationID := range stationIDs {
		celsius := targets[stationID]
		item := states[stationID]
		if item.Hold {
			s.pendingTargets[stationID] = plannedTarget{PlanID: planID, Revision: sourceRevision, Celsius: celsius}
			report.Blocked = append(report.Blocked, BlockedTarget{
				StationID: stationID, Actor: holdActor(item.HoldInfo),
				Reason: holdReason(item.HoldInfo), Version: holdVersion(item.HoldInfo), Celsius: celsius,
			})
			if s.audit != nil {
				if err := s.audit.Rejected("target.publish.blocked", stationID, actor,
					"dispatch publish refused while target is manually held", map[string]any{
						"plan_id": planID, "plan_revision": sourceRevision,
						"requested_celsius": celsius, "held_by": holdActor(item.HoldInfo),
						"hold_reason": holdReason(item.HoldInfo), "hold_version": holdVersion(item.HoldInfo),
					}); err != nil {
					return PublishReport{}, err
				}
			}
			continue
		}
		if _, err := s.registry.Update(stationID, func(item *Station) error {
			item.TargetCelsius = celsius
			item.TargetRevision = s.revision
			item.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			return PublishReport{}, err
		}
		delete(s.pendingTargets, stationID)
		report.Applied = append(report.Applied, stationID)
	}
	return report, nil
}

func validateEnvelope(stationID string, celsius float64) error {
	if celsius < 35 || celsius > 95 {
		return fmt.Errorf("target %.1f for %s outside operating envelope", celsius, stationID)
	}
	return nil
}

func holdActor(info *HoldInfo) string {
	if info == nil || info.Actor == "" {
		return "maintenance"
	}
	return info.Actor
}

func holdReason(info *HoldInfo) string {
	if info == nil {
		return ""
	}
	return info.Reason
}

func holdVersion(info *HoldInfo) uint64 {
	if info == nil {
		return 0
	}
	return info.Version
}
