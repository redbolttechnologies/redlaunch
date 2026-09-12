package store

import (
	"database/sql"
	"errors"
	"testing"
)

// TestIsUniqueConstraintUsesTypedSQLiteCode ensures unique violations are
// detected through the typed driver result code, not only message text.
func TestIsUniqueConstraintUsesTypedSQLiteCode(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE unique_probe (value TEXT UNIQUE)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO unique_probe (value) VALUES ('a')`); err != nil {
		t.Fatalf("insert probe row: %v", err)
	}
	_, rawErr := db.Exec(`INSERT INTO unique_probe (value) VALUES ('a')`)
	if rawErr == nil {
		t.Fatal("duplicate probe insert error = nil")
	}
	if !isUniqueConstraint(rawErr) {
		t.Fatalf("isUniqueConstraint(duplicate) = false, error = %v", rawErr)
	}
	wrapped := errors.Join(errors.New("probe operation failed"), rawErr)
	if !isUniqueConstraint(wrapped) {
		t.Fatalf("isUniqueConstraint(wrapped duplicate) = false, error = %v", wrapped)
	}
	if isUniqueConstraint(nil) {
		t.Fatal("isUniqueConstraint(nil) = true, want false")
	}
	if isUniqueConstraint(errors.New("something else failed")) {
		t.Fatal("isUniqueConstraint(generic) = true, want false")
	}
}
