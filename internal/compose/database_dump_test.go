package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripDumpRestrictionEnvelope(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"-- header\n\\restrict abc123\nCOPY x FROM stdin;\n\\restrict data\n\\.\n\\unrestrict abc123\n-- footer\n", "-- header\n\nCOPY x FROM stdin;\n\\restrict data\n\\.\n\n-- footer\n"},
		{"\\restrict abc\nSELECT 1;\n\\unrestrict xyz", "\\restrict abc\nSELECT 1;\n\\unrestrict xyz"},
		{"SELECT 1;\n\\restrict abc\n\\unrestrict abc", "SELECT 1;\n\\restrict abc\n\\unrestrict abc"},
		{"\\restrict abc\nSELECT 1;\n\\unrestrict abc\n\\! true", "\\restrict abc\nSELECT 1;\n\\unrestrict abc\n\\! true"},
		{"-- header\r\n\\restrict abc\r\nSELECT 1;\r\n\\unrestrict abc\r\n", "-- header\r\n\nSELECT 1;\r\n\n"},
	} {
		if got := string(stripDumpRestrictionEnvelope([]byte(test.input))); got != test.want {
			t.Fatalf("envelope handling: got %q, want %q", got, test.want)
		}
	}
}

func TestImportDatabaseDumpPassesStreamAndRestrictedArguments(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\ncase \" $* \" in *\" config \"*) printf '{}\\n';; *\" exec \"*) printf '%s\\n' \"$@\" > \"${0%/*}/args\"; cat > \"${0%/*}/stdin\";; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dump := range []string{"SELECT 'literal';\n", "PGDMPcustomfixture"} {
		if err := (CommandRunner{Binary: binary}).ImportPostgresDatabase(t.Context(), project, "postgres", "shop", "shop owner", []byte(dump)); err != nil {
			t.Fatal(err)
		}
		args, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "args"))
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "stdin"))
		if err != nil {
			t.Fatal(err)
		}
		if string(stdin) != dump || !strings.Contains(string(args), "--username=shop owner\n--dbname=shop") || strings.Contains(string(args), "sh\n") {
			t.Fatal("dump did not stream to selected role without shell")
		}
		if strings.HasPrefix(dump, "PGDMP") {
			if !strings.Contains(string(args), "pg_restore\n--no-owner\n--no-privileges\n--exit-on-error\n--single-transaction") {
				t.Fatal("unsafe custom restore flags")
			}
		} else if !strings.Contains(string(args), "\\restrict ") || !strings.Contains(string(args), "ON_ERROR_STOP=1") || !strings.HasSuffix(string(args), "-f\n-\n") {
			t.Fatal("SQL dump lacked restricted streaming mode")
		}
	}
}

func TestImportDatabaseDumpRejectsInvalidFormat(t *testing.T) {
	for _, contents := range [][]byte{nil, []byte("SELECT\x00"), {0xff, 0xfe}, []byte(strings.Repeat("x", maxDatabaseDumpBytes+1))} {
		if err := (CommandRunner{}).ImportPostgresDatabase(t.Context(), t.TempDir(), "postgres", "shop", "owner", contents); err == nil {
			t.Fatal("accepted invalid dump")
		}
	}
}
