package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSelfUpdateFixture(t *testing.T, gitScript, dockerScript string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "docker-compose.yml"), []byte("services:\n  app:\n    image: redlaunch:local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDirectory := t.TempDir()
	callsDirectory := t.TempDir()
	t.Setenv("SELF_UPDATE_CALLS_DIR", callsDirectory)

	writeExecutable := func(name, script string) {
		contents := "#!/bin/sh\n" + "echo \"$0 $@\" >> \"$SELF_UPDATE_CALLS_DIR/" + name + ".calls\"\n" + "echo \"$PWD\" >> \"$SELF_UPDATE_CALLS_DIR/" + name + ".dirs\"\n" + script + "\n"
		path := filepath.Join(binDirectory, name)
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("git", gitScript)
	writeExecutable("docker", dockerScript)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return directory
}

func readSelfUpdateCalls(t *testing.T, name string) (calls, dirs string) {
	t.Helper()
	callsDirectory := os.Getenv("SELF_UPDATE_CALLS_DIR")
	callContents, err := os.ReadFile(filepath.Join(callsDirectory, name+".calls"))
	if err != nil {
		t.Fatalf("read %s calls: %v", name, err)
	}
	dirContents, err := os.ReadFile(filepath.Join(callsDirectory, name+".dirs"))
	if err != nil {
		t.Fatalf("read %s dirs: %v", name, err)
	}
	return strings.TrimSpace(string(callContents)), strings.TrimSpace(string(dirContents))
}

func TestSelfUpdateRunsGitPullThenComposeRebuild(t *testing.T) {
	directory := writeSelfUpdateFixture(t, "exit 0", "exit 0")

	service, err := NewSelfUpdateService(directory, "git", "docker")
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	if err := service.UpdateWithProgress(context.Background(), func(stage, _ string) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0] != "pull" || stages[1] != "rebuild" {
		t.Fatalf("progress stages = %v, want [pull rebuild]", stages)
	}

	gitCalls, gitDir := readSelfUpdateCalls(t, "git")
	if !strings.HasSuffix(gitCalls, "git pull --ff-only") {
		t.Fatalf("git invocation = %q, want suffix %q", gitCalls, "git pull --ff-only")
	}
	wantDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if gitDir != wantDirectory {
		t.Fatalf("git working directory = %q, want %q", gitDir, wantDirectory)
	}
	dockerCalls, dockerDir := readSelfUpdateCalls(t, "docker")
	if !strings.HasSuffix(dockerCalls, "docker compose up -d --build") {
		t.Fatalf("docker invocation = %q, want suffix %q", dockerCalls, "docker compose up -d --build")
	}
	if dockerDir != wantDirectory {
		t.Fatalf("docker working directory = %q, want %q", dockerDir, wantDirectory)
	}
}

func TestSelfUpdateStopsWhenGitPullFails(t *testing.T) {
	directory := writeSelfUpdateFixture(t, "echo pull-failed >&2\nexit 1", "exit 0")

	service, err := NewSelfUpdateService(directory, "git", "docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "pull Redlaunch update") {
		t.Fatalf("Update() error = %v, want pull failure", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("SELF_UPDATE_CALLS_DIR"), "docker.calls")); !os.IsNotExist(err) {
		t.Fatal("docker compose ran after a failed git pull")
	}
}

func TestSelfUpdateRejectsDirectoryWithoutComposeFile(t *testing.T) {
	directory := t.TempDir()
	service, err := NewSelfUpdateService(directory, "git", "docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "Compose file") {
		t.Fatalf("Update() error = %v, want missing Compose file", err)
	}
}

func TestNewSelfUpdateServiceRejectsEmptyAndRootDirectory(t *testing.T) {
	if _, err := NewSelfUpdateService("", "git", "docker"); err == nil {
		t.Fatal("NewSelfUpdateService() with empty directory returned nil error")
	}
	if _, err := NewSelfUpdateService("/", "git", "docker"); err == nil {
		t.Fatal("NewSelfUpdateService() with filesystem root returned nil error")
	}
}
