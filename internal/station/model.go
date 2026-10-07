package station

import "time"

type ExchangerPhase string

const (
	ExchangerStandby  ExchangerPhase = "standby"
	ExchangerPriming  ExchangerPhase = "priming"
	ExchangerCarrying ExchangerPhase = "carrying"
	ExchangerDraining ExchangerPhase = "draining"
)

// HoldInfo carries the audit metadata for a manually held target. Version is
// incremented every time a hold is (re)applied to the station, so repeated
// holds with a new operator or reason are distinguishable in the audit trail.
type HoldInfo struct {
	Actor    string    `json:"actor"`
	Reason   string    `json:"reason"`
	Version  uint64    `json:"version"`
	Revision uint64    `json:"revision"`
	HeldAt   time.Time `json:"held_at"`
}

type Station struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	ZoneID          string                    `json:"zone_id"`
	Online          bool                      `json:"online"`
	Maintenance     bool                      `json:"maintenance"`
	TargetCelsius   float64                   `json:"target_celsius"`
	Hold            bool                      `json:"hold"`
	HoldInfo        *HoldInfo                 `json:"hold_info,omitempty"`
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
	if s.HoldInfo != nil {
		info := *s.HoldInfo
		copy.HoldInfo = &info
	}
	return copy
}

type Target struct {
	StationID string    `json:"station_id"`
	Celsius   float64   `json:"celsius"`
	Revision  uint64    `json:"revision"`
	Hold      bool      `json:"hold"`
	HoldInfo  *HoldInfo `json:"hold_info,omitempty"`
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
