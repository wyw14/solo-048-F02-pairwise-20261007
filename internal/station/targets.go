package station

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"heatpulse/internal/audit"
)

// errStationHeld signals inside a registry update that the station is under a
// manual hold and the publish must skip it instead of overwriting the target.
var errStationHeld = errors.New("station is manually held")

// PublishResult reports which station targets a dispatch plan actually changed
// and which were protected by a manual hold.
type PublishResult struct {
	Revision uint64   `json:"revision"`
	Applied  []string `json:"applied"`
	Held     []string `json:"held"`
}

type TargetService struct {
	registry *Registry
	audit    *audit.Recorder
	mu       sync.Mutex
	revision uint64
	// latest holds the most recently published dispatch target per station,
	// including targets that were skipped because of a manual hold. It is the
	// "latest plan" a released station is revalidated against.
	latest map[string]float64
}

func NewTargetService(registry *Registry, recorder *audit.Recorder) *TargetService {
	var revision uint64
	latest := make(map[string]float64)
	for _, item := range registry.List() {
		if item.TargetRevision > revision {
			revision = item.TargetRevision
		}
		latest[item.ID] = item.TargetCelsius
	}
	return &TargetService{registry: registry, audit: recorder, revision: revision, latest: latest}
}

func (s *TargetService) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

// ManualHold places a manual hold on every listed station. The hold carries the
// operator, a reason and the hold version (the target revision it was placed
// at); each station in the batch gets its own audit entry.
func (s *TargetService) ManualHold(stationIDs []string, actor, reason string) (uint64, error) {
	if len(stationIDs) == 0 {
		return 0, fmt.Errorf("at least one station is required")
	}
	if actor == "" {
		return 0, fmt.Errorf("hold actor is required")
	}
	if reason == "" {
		return 0, fmt.Errorf("hold reason is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stationID := range stationIDs {
		if _, ok := s.registry.Get(stationID); !ok {
			err := fmt.Errorf("station %s not found", stationID)
			_ = s.audit.Rejected("dispatch.hold", stationID, actor, err.Error(), map[string]any{"reason": reason, "stations": stationIDs})
			return 0, err
		}
	}
	s.revision++
	heldAt := time.Now().UTC()
	for _, stationID := range stationIDs {
		if _, err := s.registry.Update(stationID, func(item *Station) error {
			item.Hold = HoldState{Held: true, Actor: actor, Reason: reason, Version: s.revision, At: heldAt}
			item.TargetRevision = s.revision
			return nil
		}); err != nil {
			return 0, fmt.Errorf("hold %s by %s: %w", stationID, actor, err)
		}
		_ = s.audit.Accepted("dispatch.hold", stationID, actor, "manual hold applied", map[string]any{
			"reason": reason, "version": s.revision, "batch_size": len(stationIDs),
		})
	}
	return s.revision, nil
}

// ReleaseHold clears the manual hold on one station, then revalidates it
// against the latest published plan: a plan target skipped while the hold was
// active is validated again and applied before the release is audited.
func (s *TargetService) ReleaseHold(stationID, actor string) (uint64, error) {
	if actor == "" {
		return 0, fmt.Errorf("release actor is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.registry.Get(stationID)
	if !ok {
		return 0, fmt.Errorf("station %s not found", stationID)
	}
	if !item.Hold.Held {
		err := fmt.Errorf("station %s is not held", stationID)
		_ = s.audit.Rejected("dispatch.hold.release", stationID, actor, err.Error(), nil)
		return 0, err
	}
	released := item.Hold
	s.revision++
	details := map[string]any{
		"hold_actor":      released.Actor,
		"hold_reason":     released.Reason,
		"hold_version":    released.Version,
		"release_version": s.revision,
	}
	planned, hasPlan := s.latest[stationID]
	reapply := hasPlan && planned != item.TargetCelsius
	if reapply {
		if err := validateTarget(planned); err != nil {
			reapply = false
			details["validation_error"] = err.Error()
		}
	}
	if _, err := s.registry.Update(stationID, func(current *Station) error {
		current.Hold = HoldState{}
		current.TargetRevision = s.revision
		if reapply {
			current.TargetCelsius = planned
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if hasPlan {
		details["latest_plan_target"] = planned
		details["reapplied"] = reapply
	}
	_ = s.audit.Accepted("dispatch.hold.release", stationID, actor, "manual hold released", details)
	return s.revision, nil
}

// CompareAndPublish applies a dispatch plan at the expected source revision.
// Stations under a manual hold are never overwritten: their planned targets are
// remembered as the latest plan state and reported as held, to be revalidated
// when the hold is released.
func (s *TargetService) CompareAndPublish(sourceRevision uint64, targets map[string]float64) (PublishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sourceRevision != s.revision {
		return PublishResult{}, fmt.Errorf("dispatch source revision %d is stale; current is %d", sourceRevision, s.revision)
	}
	for stationID, celsius := range targets {
		if err := validateTarget(celsius); err != nil {
			return PublishResult{}, fmt.Errorf("station %s: %w", stationID, err)
		}
		if _, ok := s.registry.Get(stationID); !ok {
			return PublishResult{}, fmt.Errorf("station %s not found", stationID)
		}
	}
	s.revision++
	result := PublishResult{Revision: s.revision}
	for stationID, celsius := range targets {
		s.latest[stationID] = celsius
		_, err := s.registry.Update(stationID, func(item *Station) error {
			if item.Hold.Held {
				return errStationHeld
			}
			item.TargetCelsius = celsius
			item.TargetRevision = s.revision
			item.UpdatedAt = time.Now().UTC()
			return nil
		})
		if errors.Is(err, errStationHeld) {
			result.Held = append(result.Held, stationID)
			continue
		}
		if err != nil {
			return PublishResult{}, err
		}
		result.Applied = append(result.Applied, stationID)
	}
	sort.Strings(result.Applied)
	sort.Strings(result.Held)
	return result, nil
}

func validateTarget(celsius float64) error {
	if celsius < 35 || celsius > 95 {
		return fmt.Errorf("target %.1f outside operating envelope", celsius)
	}
	return nil
}
