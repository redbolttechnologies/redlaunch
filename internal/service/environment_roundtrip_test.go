package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

func environmentRoundTripSetup(t *testing.T) (*Applications, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	repository := &applicationRepositoryStub{
		applications: []application.Application{{ID: 7, Name: "Status page", FolderName: "status-page"}},
	}
	applications, err := NewApplications(repository, root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, applicationsDir, "status-page")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, varsEnvFile), []byte(""), envFileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, secretsEnvFile), []byte(""), envFileMode); err != nil {
		t.Fatal(err)
	}
	return applications, directory
}

func environmentDisplayedValue(t *testing.T, applications *Applications, name string) string {
	t.Helper()
	files, err := applications.GetEnvironmentFiles(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, variable := range append(append([]application.EnvironmentVariable{}, files.Variables...), files.Secrets...) {
		if variable.Key == name {
			return variable.Value
		}
	}
	t.Fatalf("variable %q not found in editor view", name)
	return ""
}

// TestEnvironmentLiteralDollarRoundTrip covers the R07 add → reopen → edit
// cycle: adding the literal $5 must display as $5 on reopen, resubmitting it
// must preserve bytes, and changing only the digit must encode a single
// literal dollar rather than accumulating escapes.
func TestEnvironmentLiteralDollarRoundTrip(t *testing.T) {
	applications, directory := environmentRoundTripSetup(t)
	varsPath := filepath.Join(directory, varsEnvFile)

	if err := applications.AddEnvironmentVariable(t.Context(), 7, "COST", "$5"); err != nil {
		t.Fatal(err)
	}
	stored := readServiceFile(t, varsPath)
	if !strings.Contains(stored, `COST="$$5"`) {
		t.Fatalf("stored vars.env = %q, want COST encoded as \"$$5\"", stored)
	}
	if got := environmentDisplayedValue(t, applications, "COST"); got != "$5" {
		t.Fatalf("reopened editor value = %q, want literal %q", got, "$5")
	}
	before := stored
	if err := applications.UpdateEnvironmentVariable(t.Context(), 7, "COST", "COST", "$5"); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); got != before {
		t.Fatalf("no-op edit rewrote vars.env to %q, want byte preservation", got)
	}
	if err := applications.UpdateEnvironmentVariable(t.Context(), 7, "COST", "COST", "$6"); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); !strings.Contains(got, `COST="$$6"`) || strings.Contains(got, "$$$") {
		t.Fatalf("digit-only edit stored %q, want a single encoded dollar", got)
	}
	if got := environmentDisplayedValue(t, applications, "COST"); got != "$6" {
		t.Fatalf("reopened editor value after digit edit = %q, want %q", got, "$6")
	}
}

// TestEnvironmentDollarQuotingSemantics pins the quoting contract:
// double-quoted and unquoted escapes decode, single-quoted values stay
// literal, and interpolation expressions survive a no-op untouched.
func TestEnvironmentDollarQuotingSemantics(t *testing.T) {
	for _, testCase := range []struct {
		line  string
		value string
	}{
		{`COST="$$5"`, "$5"},
		{`COST=$$5`, "$5"},
		{`COST='$$5'`, "$$5"},
		{`COST='$5'`, "$5"},
		{`COST="$5"`, "$5"},
		{`TARGET=${BASE}/api`, "${BASE}/api"},
		{`TARGET="$${BASE}/api"`, "${BASE}/api"},
		{`PLUS=$$`, "$"},
	} {
		entries := parseEnvironmentEntries(testCase.line + "\n")
		if len(entries) != 1 || entries[0].value != testCase.value {
			t.Fatalf("parse %q = %#v, want value %q", testCase.line, entries, testCase.value)
		}
	}
}

// TestEnvironmentLiteralMovePreservesValue ensures moving a literal-dollar
// entry between the two ordered environment files keeps its meaning.
func TestEnvironmentLiteralMovePreservesValue(t *testing.T) {
	applications, directory := environmentRoundTripSetup(t)
	varsPath := filepath.Join(directory, varsEnvFile)
	secretsPath := filepath.Join(directory, secretsEnvFile)

	if err := applications.AddEnvironmentVariable(t.Context(), 7, "COST", "$5"); err != nil {
		t.Fatal(err)
	}
	if err := applications.MoveEnvironmentVariableToSecrets(t.Context(), 7, "COST"); err != nil {
		t.Fatal(err)
	}
	if got := readServiceFile(t, varsPath); strings.Contains(got, "COST") {
		t.Fatalf("vars.env still contains COST after move:\n%s", got)
	}
	secrets := readServiceFile(t, secretsPath)
	if !strings.Contains(secrets, `COST="$$5"`) {
		t.Fatalf("secrets.env after move = %q, want COST encoded once", secrets)
	}
	files, err := applications.GetEnvironmentFiles(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, variable := range files.Secrets {
		if variable.Key == "COST" {
			found = true
			if variable.Value != "$5" {
				t.Fatalf("moved secret displays as %q, want literal %q", variable.Value, "$5")
			}
		}
	}
	if !found {
		t.Fatal("moved COST secret not found in editor view")
	}
}
