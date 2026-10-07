package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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
	if input.Actor == "" || input.Reason == "" {
		writeError(w, http.StatusBadRequest, errors.New("hold actor and reason are required"))
		return
	}
	revision, err := a.Targets.ManualHold(input.StationIDs, input.Actor, input.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "stations": input.StationIDs})
}

func (a *App) releaseHold(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Actor string `json:"actor"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if input.Actor == "" {
		input.Actor = r.URL.Query().Get("actor")
	}
	if input.Actor == "" {
		writeError(w, http.StatusBadRequest, errors.New("release actor is required"))
		return
	}
	revision, err := a.Targets.ReleaseHold(chi.URLParam(r, "stationID"), input.Actor)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
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
