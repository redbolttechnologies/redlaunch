package store

import (
	"testing"

	"redlaunch/internal/application"
)

func TestServerSSHKeyRoundTrip(t *testing.T) {
	database, err := Open(t.Context(), t.TempDir()+"/redlaunch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	created, err := database.CreateServerSSHKey(t.Context(), application.ServerSSHKey{
		DisplayName:    "GHA migrator workflow access",
		PublicKey:      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMtestkey1 redlaunch-ssh-key",
		KeyFingerprint: "SHA256:test1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID < 1 {
		t.Fatalf("created SSH key ID = %d, want positive", created.ID)
	}

	second, err := database.CreateServerSSHKey(t.Context(), application.ServerSSHKey{
		DisplayName:    "second key",
		PublicKey:      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMtestkey2 redlaunch-ssh-key",
		KeyFingerprint: "SHA256:test2",
		ApplicationID:  7,
		ServiceName:    "cache",
		PermitOpen:     "127.0.0.1:6379",
	})
	if err != nil {
		t.Fatal(err)
	}

	keys, err := database.ListServerSSHKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != created.ID || keys[1].ID != second.ID {
		t.Fatalf("listed SSH keys = %#v, want creation order", keys)
	}
	if keys[0].DisplayName != "GHA migrator workflow access" || keys[0].KeyFingerprint != "SHA256:test1" {
		t.Fatalf("first SSH key = %#v, want display metadata", keys[0])
	}

	got, err := database.GetServerSSHKey(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != created.PublicKey {
		t.Fatalf("fetched public key = %q, want %q", got.PublicKey, created.PublicKey)
	}
	if got.PermitOpen != "" || got.ApplicationID != 0 || got.ServiceName != "" {
		t.Fatalf("unrestricted key restriction = %+v, want empty", got)
	}

	restricted, err := database.GetServerSSHKey(t.Context(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restricted.PermitOpen != "127.0.0.1:6379" || restricted.ApplicationID != 7 || restricted.ServiceName != "cache" {
		t.Fatalf("restricted key = %+v, want stored restriction", restricted)
	}

	if err := database.DeleteServerSSHKey(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetServerSSHKey(t.Context(), created.ID); err != application.ErrSSHKeyNotFound {
		t.Fatalf("get deleted SSH key error = %v, want ErrSSHKeyNotFound", err)
	}
	remaining, err := database.ListServerSSHKeys(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != second.ID {
		t.Fatalf("remaining SSH keys = %#v, want only second", remaining)
	}
	if err := database.DeleteServerSSHKey(t.Context(), 999999); err != application.ErrSSHKeyNotFound {
		t.Fatalf("delete missing SSH key error = %v, want ErrSSHKeyNotFound", err)
	}
}
