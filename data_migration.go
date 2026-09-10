package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const dataMigrationMetadataKey = "docker_volume_to_host_mount_v1"

// migrateDataIfRequested copies the old Docker named-volume database to the
// configured database path. A completion marker is written into the copied
// database before it replaces the destination, so a restart can safely skip a
// completed migration and retry an incomplete one.
func migrateDataIfRequested(value, destination, source string) (bool, error) {
	if !strings.EqualFold(strings.TrimSpace(value), "true") {
		return false, nil
	}
	complete, err := dataMigrationComplete(destination)
	if err != nil {
		return false, err
	}
	if complete {
		return true, nil
	}
	if _, err := os.Stat(source); err != nil {
		return false, fmt.Errorf("check migration source: %w", err)
	}
	if err := copyDatabaseForMigration(source, destination); err != nil {
		return false, err
	}
	return false, nil
}

func dataMigrationComplete(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(path))
	if err != nil {
		return false, err
	}
	defer db.Close()
	var value string
	err = db.QueryRow(`SELECT value FROM app_metadata WHERE key=?`, dataMigrationMetadataKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(errString(err), "no such table") {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read migration status: %w", err)
	}
	return value == "1", nil
}

func copyDatabaseForMigration(source, destination string) (err error) {
	snapshotDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".submanager-migration-source-*")
	if err != nil {
		return fmt.Errorf("create migration source snapshot: %w", err)
	}
	defer os.RemoveAll(snapshotDirectory)
	snapshotPath := filepath.Join(snapshotDirectory, "source.db")
	if err := copyMigrationFile(source, snapshotPath); err != nil {
		return fmt.Errorf("copy migration source snapshot: %w", err)
	}
	if err := copyMigrationFileIfPresent(source+"-wal", snapshotPath+"-wal"); err != nil {
		return fmt.Errorf("copy migration source WAL snapshot: %w", err)
	}

	staging, err := os.CreateTemp(filepath.Dir(destination), ".submanager-migration-*.db")
	if err != nil {
		return fmt.Errorf("create migration staging database: %w", err)
	}
	stagingPath := staging.Name()
	if err := staging.Close(); err != nil {
		_ = os.Remove(stagingPath)
		return fmt.Errorf("close migration staging database: %w", err)
	}
	if err := os.Remove(stagingPath); err != nil {
		return fmt.Errorf("prepare migration staging database: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(stagingPath)
		}
	}()

	sourceDB, err := sql.Open("sqlite3", sqliteReadOnlyDSN(snapshotPath))
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}
	if _, err := sourceDB.Exec(`VACUUM INTO ?`, stagingPath); err != nil {
		sourceDB.Close()
		return fmt.Errorf("copy migration source: %w", err)
	}
	if err := sourceDB.Close(); err != nil {
		return fmt.Errorf("close migration source: %w", err)
	}

	stagingDB, err := sql.Open("sqlite3", stagingPath)
	if err != nil {
		return fmt.Errorf("open copied migration database: %w", err)
	}
	if _, err := stagingDB.Exec(`CREATE TABLE IF NOT EXISTS app_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO app_metadata(key,value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value=excluded.value`, dataMigrationMetadataKey); err != nil {
		stagingDB.Close()
		return fmt.Errorf("record migration completion: %w", err)
	}
	if err := stagingDB.Close(); err != nil {
		return fmt.Errorf("close copied migration database: %w", err)
	}
	if err := os.Rename(stagingPath, destination); err != nil {
		return fmt.Errorf("activate migrated database: %w", err)
	}
	return nil
}

func copyMigrationFile(source, destination string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func copyMigrationFileIfPresent(source, destination string) error {
	err := copyMigrationFile(source, destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func sqliteReadOnlyDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_busy_timeout=5000"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
