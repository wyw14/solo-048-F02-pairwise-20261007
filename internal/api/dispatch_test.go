package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestApp(t *testing.T, h http.Handler) func(string, string, any) (int, map[string]any) {
	t.Helper()
	return func(method, path string, body any) (int, map[string]any) {
		var reader *bytes.Reader
		if body != nil {
			payload, _ := json.Marshal(body)
			reader = bytes.NewReader(payload)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		var envelope struct {
			Data  map[string]any `json:"data"`
			Error string         `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		if envelope.Error != "" && envelope.Data == nil {
			envelope.Data = map[string]any{"error": envelope.Error}
		}
		return recorder.Code, envelope.Data
	}
}

func TestHoldPublishReleaseFlowOverHTTP(t *testing.T) {
	app, err := NewApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := newTestApp(t, app.Handler())

	// Batch hold with operator and reason.
	status, data := request(http.MethodPost, "/api/v1/dispatch/holds", map[string]any{
		"station_ids": []string{"xincheng"}, "actor": "li", "reason": "管网抢修",
	})
	if status != http.StatusOK {
		t.Fatalf("hold status = %d (%v)", status, data)
	}
	holds, _ := data["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %v", data["holds"])
	}
	firstHold := holds[0].(map[string]any)
	if firstHold["actor"] != "li" || firstHold["reason"] != "管网抢修" {
		t.Errorf("hold metadata = %v", firstHold)
	}

	// Hold without a reason is rejected.
	if status, _ := request(http.MethodPost, "/api/v1/dispatch/holds", map[string]any{
		"station_ids": []string{"binhe"}, "actor": "li",
	}); status != http.StatusConflict {
		t.Fatalf("expected 409 for hold without reason, got %d", status)
	}

	// Begin and publish a plan touching the held station and a free one.
	status, planData := request(http.MethodPost, "/api/v1/dispatch/plans", map[string]any{
		"targets": map[string]float64{"xincheng": 75, "binhe": 70}, "delay_millis": 0,
	})
	if status != http.StatusAccepted {
		t.Fatalf("create plan status = %d (%v)", status, planData)
	}
	planID := planData["id"].(string)

	status, published := request(http.MethodPost, "/api/v1/dispatch/plans/"+planID+"/publish", map[string]any{
		"actor": "wang",
	})
	if status != http.StatusOK {
		t.Fatalf("publish status = %d (%v)", status, published)
	}
	if published["status"] != "partial" {
		t.Fatalf("plan status = %v, want partial", published["status"])
	}

	// Release without actor/reason is rejected.
	if status, _ := request(http.MethodDelete, "/api/v1/dispatch/holds/xincheng", map[string]any{}); status != http.StatusConflict {
		t.Fatalf("expected 409 for release without actor/reason, got %d", status)
	}

	// Release the hold: the held target is revalidated against the newest plan.
	status, release := request(http.MethodDelete, "/api/v1/dispatch/holds/xincheng", map[string]any{
		"actor": "chief", "reason": "抢修结束",
	})
	if status != http.StatusOK {
		t.Fatalf("release status = %d (%v)", status, release)
	}
	if release["revalidation"] != "applied" {
		t.Fatalf("revalidation = %v", release["revalidation"])
	}
	if celsius, _ := release["celsius"].(float64); celsius != 75 {
		t.Errorf("revalidated celsius = %v, want 75", release["celsius"])
	}

	// Releasing again reports the station is no longer held.
	if status, _ := request(http.MethodDelete, "/api/v1/dispatch/holds/xincheng", map[string]any{
		"actor": "chief", "reason": "再次释放",
	}); status != http.StatusConflict {
		t.Fatalf("expected 409 releasing a non-held station, got %d", status)
	}
}
