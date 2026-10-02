package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
	"vibexui/internal/model"
)

type Store struct {
	mu sync.Mutex
	db *sql.DB
	// Committed JSON is cached; every caller receives its own decoded copy.
	cached []byte
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), data TEXT NOT NULL); INSERT OR IGNORE INTO state VALUES(1,'{"servers":[],"nodes":[],"clients":[]}'); CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error               { return s.db.Close() }
func (s *Store) View() (model.State, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.read() }
func (s *Store) read() (model.State, error) {
	if s.cached == nil {
		var raw string
		if err := s.db.QueryRow("SELECT data FROM state WHERE id=1").Scan(&raw); err != nil {
			return model.State{}, err
		}
		s.cached = []byte(raw)
	}
	var st model.State
	err := json.Unmarshal(s.cached, &st)
	return st, err
}
func (s *Store) Update(fn func(*model.State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.read()
	if err != nil {
		return err
	}
	if err = fn(&st); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, s.cached) {
		return nil
	}
	_, err = s.db.Exec("UPDATE state SET data=? WHERE id=1", string(raw))
	if err == nil {
		s.cached = raw
	}
	return err
}
func (s *Store) Setting(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}
func (s *Store) SetSetting(key, val string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, val)
	return err
}

// SetCredentials updates both values in a single atomic SQLite statement.
func (s *Store) SetCredentials(username, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("INSERT INTO settings(key,value) VALUES('username',?),('password',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", username, passwordHash)
	return err
}
