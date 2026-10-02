package store

import (
	"path/filepath"
	"testing"
)

func TestCredentialsUpdateIsAtomic(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetCredentials("old-name", "old-hash"); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`CREATE TRIGGER reject_password BEFORE UPDATE ON settings WHEN NEW.key='password' BEGIN SELECT RAISE(ABORT,'test failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetCredentials("new-name", "new-hash"); err == nil {
		t.Fatal("expected save failure")
	}
	name, _ := s.Setting("username")
	hash, _ := s.Setting("password")
	if name != "old-name" || hash != "old-hash" {
		t.Fatal("partial credential update committed")
	}
}
