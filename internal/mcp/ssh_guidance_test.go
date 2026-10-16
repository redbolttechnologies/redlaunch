package mcp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSSHWorkflowGuidanceDiscovery(t *testing.T) {
	server, err := New("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	responses := session(t, server, handshake+`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	var initialized struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(responses[0].Result, &initialized); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initialized.Instructions, sshWorkflowGuidance) {
		t.Fatal("initialization must explain managed-file access before agents create SSH workflows")
	}
	var listed struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(responses[1].Result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if !strings.Contains(tool.Description, sshWorkflowGuidance) {
			t.Errorf("offline tool %s lacks SSH filesystem guidance", tool.Name)
		}
	}
}

func TestBundledSSHComposeFileChecks(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	// Exercise every documented selection block using a container-only path.
	// Host-side checks fail even though the mock manager can see the file.
	selection := regexp.MustCompile(`(?m)^ *if [^\n]*\$DEPLOY_DIR/compose\.yml[^\n]*\n`)
	for _, guide := range []struct {
		topic string
		count int
	}{{"recipes", 4}, {"ssh-keys", 1}} {
		content, ok := readGuide(guide.topic)
		if !ok {
			t.Fatalf("missing bundled guide %s", guide.topic)
		}
		blocks := selection.FindAllStringIndex(content, -1)
		if len(blocks) != guide.count {
			t.Fatalf("%s: found %d file selection blocks, want %d", guide.topic, len(blocks), guide.count)
		}
		for i, block := range blocks {
			start := block[0]
			end := strings.Index(content[start:], "project_name=$(")
			if end < 0 {
				t.Fatal("selection missing managed project identity lookup")
			}
			script := "set -euo pipefail\n" + content[start:start+end] + "printf '%s\\n' \"$compose_file\"\n"
			for _, filename := range []string{"compose.yml", "compose.yaml", "missing"} {
				t.Run(guide.topic+"/"+string(rune('1'+i))+"/"+filename, func(t *testing.T) {
					directory := t.TempDir()
					stub := `#!/bin/bash
set -euo pipefail
[[ "$#" == 5 && "$1" == exec && "$2" == redbolt-redlaunch && "$3" == test && "$4" == -f ]] || exit 2
[[ "$5" == "$DEPLOY_DIR/$TEST_COMPOSE_FILE" ]]
`
					if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(stub), 0700); err != nil {
						t.Fatal(err)
					}
					command := exec.Command(bash, "-c", script)
					command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "DEPLOY_DIR="+filepath.Join(directory, "container-only app"), "TEST_COMPOSE_FILE="+filename)
					output, err := command.CombinedOutput()
					if filename == "missing" {
						if err == nil || !strings.Contains(string(output), "inside redbolt-redlaunch") {
							t.Fatalf("missing file should fail with container context: err=%v output=%s", err, output)
						}
					} else if err != nil || string(output) != filename+"\n" {
						t.Fatalf("container file not selected: err=%v output=%s", err, output)
					}
				})
			}
		}
	}
}
