package minigames

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ridgericetalk/internal/model"
)

func TestSubmitScoreAndLeaderboard(t *testing.T) {
	_, r, db := setupTestHandler(t, nil)
	seedMembership(t, db, "space-1", "user-1")

	// Submit a score
	body := map[string]interface{}{
		"gameType": "2048",
		"score":    128,
	}
	jsonBody, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/minigames/leaderboard", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	// Submit a lower score should not update
	body["score"] = 64
	jsonBody, _ = json.Marshal(body)
	req = httptest.NewRequest("POST", "/api/v1/minigames/leaderboard", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data["bestScore"].(float64) != 128 {
		t.Fatalf("expected bestScore to remain 128, got %v", data["bestScore"])
	}

	// Zero score should be rejected
	body["score"] = 0
	jsonBody, _ = json.Marshal(body)
	req = httptest.NewRequest("POST", "/api/v1/minigames/leaderboard", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for zero score, got %d", w.Code)
	}

	// Get leaderboard
	req2 := httptest.NewRequest("GET", "/api/v1/minigames/leaderboard?gameType=2048", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d, body: %s", w2.Code, w2.Body.String())
	}
	var leaderboardResp map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &leaderboardResp); err != nil {
		t.Fatalf("failed to parse leaderboard: %v", err)
	}
	ldata, _ := leaderboardResp["data"].(map[string]interface{})
	entries, _ := ldata["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 leaderboard entry, got %d", len(entries))
	}

	// Verify only one record exists in DB.
	var count int64
	db.Model(&model.MinigameLeaderboard{}).Where("game_type = ?", "2048").Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 DB record, got %d", count)
	}
}
