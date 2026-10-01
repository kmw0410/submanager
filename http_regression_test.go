package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNextPaymentClampsEveryPeriod(t *testing.T) {
	for _, tt := range []struct {
		now, anchor, cycle, want string
		day                      int
	}{
		{"2025-02-01", "2025-01-31", "monthly", "2025-02-28", 31},
		{"2024-02-01", "2024-01-31", "monthly", "2024-02-29", 31},
		{"2025-03-01", "2025-01-31", "monthly", "2025-03-31", 31},
		{"2025-03-01", "2024-02-29", "yearly", "2026-02-28", 29},
		{"2025-02-28", "2024-02-29", "yearly", "2025-02-28", 29},
		{"2025-01-01", "2027-08-31", "yearly", "2027-08-31", 31},
		{"2025-01-01", "2025-02-15", "monthly", "2025-02-15", 15},
	} {
		t.Run(tt.now+tt.anchor, func(t *testing.T) {
			now, _ := time.ParseInLocation("2006-01-02", tt.now, time.FixedZone("Asia/Seoul", 9*3600))
			if got := nextPayment(now, tt.day, tt.cycle, tt.anchor); got != tt.want {
				t.Fatalf("date=%s want=%s", got, tt.want)
			}
		})
	}
}

func TestDecodeRejectsMultipleJSONDocuments(t *testing.T) {
	for _, body := range []string{`null`, `{"name":"ok"}{}`, `{"name":"ok"} garbage`, `{"unknown":true}`, `{"name":`} {
		var input struct{ Name string }
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		if decode(w, r, &input) || w.Code != http.StatusBadRequest {
			t.Fatalf("invalid input status=%d", w.Code)
		}
	}
	var input struct{ Name string }
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{\"name\":\"ok\"} \n\t"))
	w := httptest.NewRecorder()
	if !decode(w, r, &input) || input.Name != "ok" {
		t.Fatal("trailing whitespace should be allowed")
	}
	// A second valid document is rejected even when its decoding would succeed.
	d := json.NewDecoder(strings.NewReader(`{} []`))
	if decodeJSONDocument(d, &input) == nil {
		t.Fatal("extra JSON accepted")
	}
}
