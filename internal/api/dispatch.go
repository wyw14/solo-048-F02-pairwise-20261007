package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"heatpulse/internal/station"
)

func (a *App) listPlans(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.Planner.Plans())
}

func (a *App) createPlan(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Targets     map[string]float64 `json:"targets"`
		DelayMillis int                `json:"delay_millis"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := a.Planner.Begin(r.Context(), input.Targets, time.Duration(input.DelayMillis)*time.Millisecond)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, plan)
}

func (a *App) publishPlan(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Actor string `json:"actor"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan, err := a.Publisher.Publish(chi.URLParam(r, "planID"), input.Actor)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (a *App) applyHold(w http.ResponseWriter, r *http.Request) {
	var input struct {
		StationIDs []string `json:"station_ids"`
		Actor      string   `json:"actor"`
		Reason     string   `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	batch, err := a.Targets.ManualHold(station.HoldRequest{
		StationIDs: input.StationIDs, Actor: input.Actor, Reason: input.Reason,
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, batch)
}

func (a *App) releaseHold(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Actor  string `json:"actor"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := a.Targets.ReleaseHold(chi.URLParam(r, "stationID"), input.Actor, input.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) bufferCommand(w http.ResponseWriter, r *http.Request) {
	var input struct {
		StationID string  `json:"station_id"`
		Epoch     uint64  `json:"epoch"`
		Sequence  uint64  `json:"sequence"`
		Celsius   float64 `json:"celsius"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	command, err := a.DispatchBuffer.Enqueue(input.StationID, input.Epoch, input.Sequence, input.Celsius)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, command)
}
