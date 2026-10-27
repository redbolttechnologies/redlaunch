package main

import (
	"os"
	"strings"
	"testing"
)

// The production image only receives the files explicitly copied by the
// Dockerfile, so every file embedded by documentation.go must also appear in
// that copy list. Missing one fails the image build with an embed error.
func TestDockerfileCopiesEmbeddedDocumentation(t *testing.T) {
	source, err := os.ReadFile("../../documentation.go")
	if err != nil {
		t.Fatal(err)
	}
	const directive = "//go:embed "
	start := strings.Index(string(source), directive)
	if start < 0 {
		t.Fatal("documentation.go has no go:embed directive")
	}
	line := string(source[start+len(directive):])
	line, _, _ = strings.Cut(line, "\n")

	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var copies []string
	for _, dockerfileLine := range strings.Split(string(dockerfile), "\n") {
		if strings.HasPrefix(dockerfileLine, "COPY ") {
			copies = append(copies, " "+dockerfileLine+" ")
		}
	}

	for _, name := range append(strings.Fields(line), "documentation.go") {
		found := false
		for _, copyLine := range copies {
			if strings.Contains(copyLine, " "+name+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Dockerfile does not copy %s, which the build requires", name)
		}
	}
}
