package handler

import (
	"testing"
	"time"

	"redlaunch/internal/application"
)

func TestGitHubActionsJobStoreExpiresCompletedHandoffs(t *testing.T) {
	store := newGitHubActionsJobStore()
	job, created, err := store.create(7, githubActionsJobOperationConfigure)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first GitHub Actions job was not created")
	}
	job.complete(application.GitHubActionsSetup{
		Integration: application.GitHubActionsIntegration{ApplicationID: 7, PublicKey: "ssh-ed25519 public"},
		PrivateKey:  "private-key",
	})

	job.mu.Lock()
	job.finishedAt = time.Now().Add(-githubActionsJobRetention - time.Minute)
	job.mu.Unlock()

	if got := store.get(7, job.id); got != nil {
		t.Fatal("expired GitHub Actions handoff is still available")
	}
}
