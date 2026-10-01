package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateDataIfRequestedCopiesOnceAndSkipsAfterCompletion(t *testing.T) {
	directory := t.TempDir()
	sourceDirectory := filepath.Join(directory, "source")
	destinationDirectory := filepath.Join(directory, "data")
	if err := os.Mkdir(sourceDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destinationDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceDirectory, "source.db")
	destinationPath := filepath.Join(destinationDirectory, "submanager.db")
	source, err := sql.Open("sqlite3", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE example (value TEXT NOT NULL); INSERT INTO example(value) VALUES ('from-volume')`); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDirectory, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sourceDirectory, 0o755) })

	alreadyComplete, err := migrateDataIfRequested("true", destinationPath, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyComplete {
		t.Fatal("first migration was unexpectedly skipped")
	}
	destination, err := sql.Open("sqlite3", destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := destination.QueryRow(`SELECT value FROM example`).Scan(&value); err != nil || value != "from-volume" {
		t.Fatalf("migrated data = %q, %v", value, err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}

	alreadyComplete, err = migrateDataIfRequested("true", destinationPath, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !alreadyComplete {
		t.Fatal("completed migration was not skipped")
	}
}

func TestMigrateDataIfRequestedLeavesRetryableStateOnFailure(t *testing.T) {
	destinationPath := filepath.Join(t.TempDir(), "submanager.db")
	if _, err := migrateDataIfRequested("true", destinationPath, filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing source did not fail")
	}
	complete, err := dataMigrationComplete(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("failed migration was marked complete")
	}
}
