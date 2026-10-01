package main

import (
	"bytes"
	"database/sql"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSQLiteDriverIsRegistered(t *testing.T) {
	for _, driver := range sql.Drivers() {
		if driver == "sqlite3" {
			return
		}
	}
	t.Fatal("sqlite3 driver is not registered")
}

func TestLoggingSkipsHealthChecks(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})

	handler := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if output.Len() != 0 {
		t.Fatalf("health check was logged: %q", output.String())
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if !strings.Contains(output.String(), "GET /api/state") {
		t.Fatalf("ordinary request was not logged: %q", output.String())
	}
}
