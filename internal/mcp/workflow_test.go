package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedWorkflowExecutesSafely(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	workflow := runWorkflow(7, "migrate")
	_, body, ok := strings.Cut(workflow, "        run: |\n")
	if !ok {
		t.Fatal("missing workflow script")
	}
	// Remove only the YAML indentation, preserving Python block indentation.
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, "          ")
	}
	script := strings.Join(lines, "\n")
	cases := []struct {
		name, url, status, jobID string
		success                  bool
	}{
		{"complete", "https://example.com", "complete", "job-1", true},
		{"loopback", "http://127.0.0.1:8080/", "complete", "job-1", true},
		{"failed", "https://example.com", "failed", "job-1", false},
		{"unknown status", "https://example.com", "unexpected", "job-1", false},
		{"invalid job", "https://example.com", "complete", "../other", false},
		{"plaintext", "http://example.com", "complete", "job-1", false},
		{"url credentials", "https://user:pass@example.com", "complete", "job-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			// This executable does not open sockets or record the Authorization header.
			stub := `#!/bin/bash
case "${!#}" in
  */run) printf '{"status":"running","job_id":"%s"}' "$TEST_JOB_ID" ;;
  */run/status\?id=*) printf '{"status":"%s","detail":"synthetic-private-output"}' "$TEST_STATUS" ;;
  *) exit 2 ;;
esac
`
			if err := os.WriteFile(filepath.Join(directory, "curl"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(bash, "-c", script)
			command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "REDLAUNCH_URL="+tc.url, "REDLAUNCH_RUN_TOKEN="+testToken(), "APPLICATION_ID=7", "SERVICE_NAME=migrate", "TEST_STATUS="+tc.status, "TEST_JOB_ID="+tc.jobID)
			output, err := command.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("workflow success=%v, want %v; output=%s", err == nil, tc.success, output)
			}
			if strings.Contains(string(output), testToken()) || strings.Contains(string(output), "synthetic-private-output") {
				t.Fatal("workflow leaked secret or service output")
			}
		})
	}
}
