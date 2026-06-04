// SPDX-License-Identifier: GPL-3.0-only

// State persistence for the assistant lives in a single SQLite database
// (state.db) inside the configured state directory. It replaces the former
// per-domain JSON/NDJSON files (usage.json, schedules.json, gmail_seen.json,
// telegram_offset.json, transcripts.ndjson) and is shared with the SDK durable
// memory store (projectstate) so every component opens one source of truth.
//
// The pure-Go modernc.org/sqlite driver is used deliberately: it needs no cgo,
// so the assistant still builds CGO_ENABLED=0 and cross-compiles cleanly.

package assistant

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// stateDBFileName is the SQLite database created inside the state directory.
const stateDBFileName = "state.db"

// stateDBBusyTimeoutMS bounds how long a writer waits for the WAL write lock
// before returning SQLITE_BUSY. It absorbs brief contention between the
// transcript writer, the scheduler, and the memory store without surfacing
// transient lock errors to callers.
const stateDBBusyTimeoutMS = 5000

// stateDBSchemaVersion is the current migration target. Bump it and append to
// the migration steps when the schema changes.
const stateDBSchemaVersion = 1

var (
	stateDBMu sync.Mutex
	// stateDBs caches one open handle per absolute state directory so the
	// transcript writer, scheduler, usage accountant, and memory store in a
	// single process share one connection (and its WAL write lock) instead of
	// racing through separate file handles. Keyed by absolute dir.
	stateDBs = map[string]*sql.DB{}
)

// stateDBFor returns the shared state database for cfg's state directory,
// opening and migrating it on first use.
func stateDBFor(cfg appConfig) (*sql.DB, error) {
	dir := strings.TrimSpace(cfg.StateDir)
	if dir == "" {
		dir = defaultStateDir()
	}
	return stateDBForDir(dir)
}

// stateDBForDir returns the shared state database located in dir, opening and
// migrating it on first use. Handles are cached per absolute directory.
func stateDBForDir(dir string) (*sql.DB, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = defaultStateDir()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	stateDBMu.Lock()
	defer stateDBMu.Unlock()
	if db, ok := stateDBs[dir]; ok {
		return db, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, stateDBFileName)
	db, err := openStateDB(path)
	if err != nil {
		return nil, err
	}
	if err := migrateStateDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The database holds conversation transcripts and durable memory; keep it
	// owner-only, matching the 0600 mode of the JSON files it replaces.
	_ = os.Chmod(path, 0o600)
	stateDBs[dir] = db
	return db, nil
}

// resetStateDBs closes and clears the cached handles. It exists for tests so
// each test starts from a clean process-level state.
func resetStateDBs() {
	stateDBMu.Lock()
	for _, db := range stateDBs {
		_ = db.Close()
	}
	stateDBs = map[string]*sql.DB{}
	stateDBMu.Unlock()

	transcriptImportMu.Lock()
	transcriptImported = map[string]bool{}
	transcriptImportMu.Unlock()
}

// openStateDB opens a SQLite handle at path with the pragmas required for safe
// concurrent use. Pragmas are passed via the DSN so they apply to every
// connection in the pool.
func openStateDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?" + url.Values{
		// _txlock=immediate makes every sql.Tx issue BEGIN IMMEDIATE, acquiring
		// the WAL write lock up front so multi-statement mutations never fail
		// part-way with SQLITE_BUSY (they wait out busy_timeout instead).
		"_txlock": []string{"immediate"},
		"_pragma": []string{
			fmt.Sprintf("busy_timeout(%d)", stateDBBusyTimeoutMS),
			"journal_mode(WAL)",
			"synchronous(NORMAL)",
			"foreign_keys(on)",
		},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes writers, which both avoids SQLITE_BUSY
	// convoys under WAL and matches the single-writer (mutex-serialized)
	// contract the former JSON stores relied on.
	db.SetMaxOpenConns(1)
	return db, nil
}

// migrateStateDB brings the database up to stateDBSchemaVersion. Each step is
// idempotent and the whole run executes inside a single immediate transaction,
// so concurrent openers either see a fully-migrated schema or wait on the write
// lock — never a half-applied one.
func migrateStateDB(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS assistant_schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM assistant_schema_version`).Scan(&current); err != nil {
		return err
	}
	steps := []string{
		// Step 1: transcript turns (one row per turn) and a generic key/value
		// table for the small whole-document states (usage, schedules,
		// gmail_seen, telegram_offset).
		`CREATE TABLE IF NOT EXISTS assistant_transcript_turns (
			id         TEXT PRIMARY KEY,
			session_id TEXT,
			started_at INTEGER,
			ended_at   INTEGER,
			data       TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS assistant_transcript_turns_session
			ON assistant_transcript_turns(session_id)`,
		`CREATE TABLE IF NOT EXISTS assistant_kv (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	if current < 1 {
		for _, stmt := range steps {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
	}
	if current < stateDBSchemaVersion {
		if _, err := tx.Exec(`DELETE FROM assistant_schema_version`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO assistant_schema_version (version) VALUES (?)`, stateDBSchemaVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// kvGet loads the JSON value stored under key into target. It reports whether
// the key was present.
func kvGet(db *sql.DB, key string, target any) (bool, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM assistant_kv WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return true, err
	}
	return true, nil
}

// kvPut stores value as JSON under key, replacing any existing row.
func kvPut(db *sql.DB, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO assistant_kv (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(data))
	return err
}

// kvGetOrImport loads key into target. When the key is absent it falls back to
// the legacy JSON file at legacyPath, persists it under key, and renames the
// legacy file to <path>.bak so the import runs at most once. It reports whether
// any value (from the DB or the legacy file) was found.
func kvGetOrImport(db *sql.DB, key, legacyPath string, target any) (bool, error) {
	found, err := kvGet(db, key, target)
	if err != nil || found {
		return found, err
	}
	exists, err := readJSONFile(legacyPath, target)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if err := kvPut(db, key, target); err != nil {
		return false, err
	}
	_ = os.Rename(legacyPath, legacyPath+".bak")
	return true, nil
}
