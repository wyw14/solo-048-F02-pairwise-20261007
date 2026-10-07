package station

import "time"

type ExchangerPhase string

const (
	ExchangerStandby  ExchangerPhase = "standby"
	ExchangerPriming  ExchangerPhase = "priming"
	ExchangerCarrying ExchangerPhase = "carrying"
	ExchangerDraining ExchangerPhase = "draining"
)

// HoldState records who placed a manual hold, why, and the target revision at
// which it was placed, so hold and release decisions stay auditable.
type HoldState struct {
	Held    bool      `json:"held"`
	Actor   string    `json:"actor,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Version uint64    `json:"version,omitempty"`
	At      time.Time `json:"at,omitempty"`
}

type Station struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	ZoneID          string                    `json:"zone_id"`
	Online          bool                      `json:"online"`
	Maintenance     bool                      `json:"maintenance"`
	TargetCelsius   float64                   `json:"target_celsius"`
	Hold            HoldState                 `json:"hold"`
	TargetRevision  uint64                    `json:"target_revision"`
	ControllerEpoch uint64                    `json:"controller_epoch"`
	Exchangers      map[string]ExchangerPhase `json:"exchangers"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

func (s Station) Clone() Station {
	copy := s
	copy.Exchangers = make(map[string]ExchangerPhase, len(s.Exchangers))
	for id, phase := range s.Exchangers {
		copy.Exchangers[id] = phase
	}
	return copy
}

type Target struct {
	StationID string    `json:"station_id"`
	Celsius   float64   `json:"celsius"`
	Revision  uint64    `json:"revision"`
	Hold      HoldState `json:"hold"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Command struct {
	ID        string    `json:"id"`
	StationID string    `json:"station_id"`
	Epoch     uint64    `json:"epoch"`
	Sequence  uint64    `json:"sequence"`
	Celsius   float64   `json:"celsius"`
	Status    string    `json:"status"`
	At        time.Time `json:"at"`
}
