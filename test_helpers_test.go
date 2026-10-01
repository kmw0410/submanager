package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestApplication(t *testing.T) *application {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "submanager.db")+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	loc, _ := time.LoadLocation("Asia/Seoul")
	a := &application{db: db, location: loc, setupToken: "test-setup-token", authLimiter: newAttemptLimiter()}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	return a
}

func jsonRequest(t *testing.T, method, target string, value any) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(method, target, bytes.NewReader(body)), httptest.NewRecorder()
}

func compactSource(source string) string {
	return strings.Join(strings.Fields(source), "")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
