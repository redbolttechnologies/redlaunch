package store

import (
	"errors"
	"path/filepath"
	"redlaunch/internal/application"
	"sync"
	"testing"
)

func TestConcurrentDeletionCannotRemoveLastUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	first, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, err := first.CreateUser(t.Context(), "a@example.com", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := first.CreateUser(t.Context(), "b@example.com", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, database := range []*Store{first, second} {
		id := []int64{a.ID, b.ID}[i]
		wg.Go(func() { <-start; results <- database.DeleteUser(t.Context(), id) })
	}
	close(start)
	wg.Wait()
	close(results)
	success, last := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, application.ErrLastUser) {
			last++
		} else {
			t.Fatal(err)
		}
	}
	list, err := first.ListUsers(t.Context())
	if err != nil || len(list) != 1 || success != 1 || last != 1 {
		t.Fatal("last-user invariant broken", err)
	}
	if _, err := first.db.ExecContext(t.Context(), `DELETE FROM users`); err == nil {
		t.Fatal("database trigger allowed deletion of final user")
	}
}
