package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServiceVolumeCandidates(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		want         []string
	}{
		{"exclusive", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"},{"type":"bind","source":"/host"},{"type":"volume"}]}},"volumes":{"data":{}}}`, []string{"project_data"}},
		{"shared", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]},"web":{"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{}}}`, []string{}},
		{"external", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{"external":true}}}`, []string{}},
		{"explicit name", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{"name":"resolved-data"}}}`, []string{"resolved-data"}},
		{"alias", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]},"web":{"volumes":[{"type":"volume","source":"alias"}]}},"volumes":{"data":{"name":"same"},"alias":{"name":"same","external":true}}}`, []string{}},
		{"unused external alias", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{"name":"same"},"alias":{"name":"same","external":true}}}`, []string{}},
		{"unsafe name", `{"services":{"db":{"volumes":[{"type":"volume","source":"data"}]}},"volumes":{"data":{"name":"--force"}}}`, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := serviceVolumeCandidates([]byte(tc.config), "project", "db")
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := serviceVolumeCandidates([]byte(`{"services":{}}`), "project", "missing"); err == nil {
		t.Fatal("missing service accepted")
	}
	if _, err := serviceVolumeCandidates([]byte(`invalid`), "project", "db"); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestServiceVolumeRemovalChecksOwnershipAndIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name, owner                         string
		exists, fails, wantErr, wantRemoval bool
	}{
		{"owned", "project", true, false, false, true},
		{"foreign", "foreign", true, false, true, false},
		{"missing", "project", false, false, false, false},
		{"in use", "project", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services:\n  db:\n    image: postgres:17\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			owner := tc.owner
			if owner == "project" {
				owner = ProjectName(directory)
			}
			listing := ""
			if tc.exists {
				listing = "data"
			}
			removal := "exit 0"
			if tc.fails {
				removal = "exit 1"
			}
			log := filepath.Join(directory, "removed")
			script := "#!/bin/sh\ncase \"$1\" in\ncompose) echo '{\"services\":{\"db\":{\"volumes\":[{\"type\":\"volume\",\"source\":\"data\"}]}},\"volumes\":{\"data\":{\"name\":\"data\"}}}';;\nvolume) case \"$2\" in\nls) echo '" + listing + "';;\ninspect) echo '{\"com.docker.compose.project\":\"" + owner + "\"}';;\nrm) echo \"$3\" >> '" + log + "'; " + removal + ";;\nesac;;\nesac\n"
			binary := filepath.Join(directory, "docker-fake")
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := CommandRunner{Binary: binary}
			err := runner.RemoveServiceVolumes(t.Context(), directory, "db", []string{"data"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("remove error = %v, wantErr %v", err, tc.wantErr)
			}
			contents, _ := os.ReadFile(log)
			if strings.Contains(string(contents), "data") != tc.wantRemoval {
				t.Fatalf("removal log = %q", contents)
			}
			if err := runner.RemoveServiceVolumes(t.Context(), directory, "db", []string{"forged"}); err == nil {
				t.Fatal("forged volume accepted")
			}
		})
	}
}
