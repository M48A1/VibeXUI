package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"vibexui/internal/model"
)

func TestCachedStateIsolationAndNoopWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Update(func(st *model.State) error { st.Servers = []model.Server{{ID: "a", Version: 1}}; return nil }); err != nil {
		t.Fatal(err)
	}
	changes := func() int {
		var n int
		if e := s.db.QueryRow("SELECT total_changes()").Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	before := changes()
	for i := 0; i < 10; i++ {
		if err = s.Update(func(st *model.State) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if changes() != before {
		t.Fatal("unchanged maintenance wrote to SQLite")
	}
	copy, err := s.View()
	if err != nil {
		t.Fatal(err)
	}
	copy.Servers[0].Version = 99
	if err = s.Update(func(st *model.State) error { st.Servers[0].Version = 100; return fmt.Errorf("reject") }); err == nil {
		t.Fatal("expected rejected update")
	}
	stored, _ := s.View()
	if stored.Servers[0].Version != 1 {
		t.Fatal("view or rejected mutation poisoned cache")
	}
	if _, err = s.db.Exec("CREATE TRIGGER fail_state BEFORE UPDATE ON state BEGIN SELECT RAISE(FAIL, 'test write failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(st *model.State) error { st.Servers[0].Version = 2; return nil }); err == nil {
		t.Fatal("expected database failure")
	}
	stored, _ = s.View()
	if stored.Servers[0].Version != 1 {
		t.Fatal("failed commit advanced cache")
	}
}
