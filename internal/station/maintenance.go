package station

import (
	"fmt"
	"sync"
	"time"

	"heatpulse/internal/store"
)

type MaintenanceSession struct {
	StationID      string    `json:"station_id"`
	EnteredAt      time.Time `json:"entered_at"`
	PreviousTarget float64   `json:"previous_target"`
	PreviousHold   HoldState `json:"previous_hold"`
	PreviousOnline bool      `json:"previous_online"`
}

type MaintenanceService struct {
	registry  *Registry
	snapshots *store.StationSnapshotStore
	mu        sync.Mutex
	sessions  map[string]MaintenanceSession
}

func NewMaintenanceService(registry *Registry, snapshots *store.StationSnapshotStore) *MaintenanceService {
	return &MaintenanceService{registry: registry, snapshots: snapshots, sessions: make(map[string]MaintenanceSession)}
}

func (s *MaintenanceService) Enter(stationID string) (MaintenanceSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[stationID]; exists {
		return MaintenanceSession{}, fmt.Errorf("station %s already in maintenance", stationID)
	}
	item, ok := s.registry.Get(stationID)
	if !ok {
		return MaintenanceSession{}, fmt.Errorf("station %s not found", stationID)
	}
	session := MaintenanceSession{
		StationID: stationID, EnteredAt: time.Now().UTC(), PreviousTarget: item.TargetCelsius,
		PreviousHold: item.Hold, PreviousOnline: item.Online,
	}
	updated, err := s.registry.Update(stationID, func(current *Station) error {
		current.Maintenance = true
		current.Hold = HoldState{Held: true, Actor: "maintenance", Reason: "maintenance session", At: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return MaintenanceSession{}, err
	}
	if err := s.snapshots.Put(store.StationSnapshot{
		StationID: stationID, Online: updated.Online, Maintenance: true,
		TargetCelsius: updated.TargetCelsius, ControllerEpoch: updated.ControllerEpoch,
	}); err != nil {
		return MaintenanceSession{}, err
	}
	s.sessions[stationID] = session
	return session, nil
}

func (s *MaintenanceService) Exit(stationID string) (Station, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, exists := s.sessions[stationID]
	if !exists {
		return Station{}, fmt.Errorf("station %s has no maintenance session", stationID)
	}
	updated, err := s.registry.Update(stationID, func(current *Station) error {
		current.Maintenance = false
		current.Hold = session.PreviousHold
		current.TargetCelsius = session.PreviousTarget
		current.Online = session.PreviousOnline
		return nil
	})
	if err != nil {
		return Station{}, err
	}
	delete(s.sessions, stationID)
	return updated, nil
}

func (s *MaintenanceService) Active(stationID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[stationID]
	return ok
}
