package service

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"redlaunch/internal/application"
)

type fakeServerSSHKeyRepository struct {
	keys   map[int64]application.ServerSSHKey
	nextID int64
}

func newFakeServerSSHKeyRepository() *fakeServerSSHKeyRepository {
	return &fakeServerSSHKeyRepository{keys: make(map[int64]application.ServerSSHKey), nextID: 1}
}

func (r *fakeServerSSHKeyRepository) ListServerSSHKeys(context.Context) ([]application.ServerSSHKey, error) {
	items := make([]application.ServerSSHKey, 0, len(r.keys))
	for _, key := range r.keys {
		items = append(items, key)
	}
	// Keep deterministic order for assertions.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].ID < items[j-1].ID; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	return items, nil
}

func (r *fakeServerSSHKeyRepository) GetServerSSHKey(_ context.Context, id int64) (application.ServerSSHKey, error) {
	key, ok := r.keys[id]
	if !ok {
		return application.ServerSSHKey{}, application.ErrSSHKeyNotFound
	}
	return key, nil
}

func (r *fakeServerSSHKeyRepository) CreateServerSSHKey(_ context.Context, item application.ServerSSHKey) (application.ServerSSHKey, error) {
	item.ID = r.nextID
	r.nextID++
	r.keys[item.ID] = item
	return item, nil
}

func (r *fakeServerSSHKeyRepository) DeleteServerSSHKey(_ context.Context, id int64) error {
	if _, ok := r.keys[id]; !ok {
		return application.ErrSSHKeyNotFound
	}
	delete(r.keys, id)
	return nil
}

type fakeServerSSHKeyGenerator struct {
	index int
}

func (g *fakeServerSSHKeyGenerator) Generate(context.Context, string) (string, string, error) {
	g.index++
	keyData := base64.StdEncoding.EncodeToString([]byte{byte(g.index), 9, 8, 7, 6, 5, 4, 3})
	return "private-key-" + string(rune('0'+g.index)), "ssh-ed25519 " + keyData + " generated", nil
}

func TestServerSSHKeyCreateInstallsPublicKey(t *testing.T) {
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	repository := newFakeServerSSHKeyRepository()
	service, err := NewServerSSHKeyService(authorizedKeys, "deploy", repository, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}

	setup, err := service.Create(t.Context(), "GHA migrator workflow access")
	if err != nil {
		t.Fatal(err)
	}
	if setup.PrivateKey == "" || !strings.HasPrefix(setup.PrivateKey, "private-key-") {
		t.Fatalf("private key was not returned transiently: %q", setup.PrivateKey)
	}
	if setup.Key.DisplayName != "GHA migrator workflow access" {
		t.Fatalf("display name = %q, want trimmed input", setup.Key.DisplayName)
	}
	if !strings.HasPrefix(setup.Key.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(setup.Key.KeyFingerprint, "SHA256:") {
		t.Fatalf("public key was not fingerprinted: %#v", setup.Key)
	}

	contents, err := os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(setup.Key.PublicKey)
	expected := fields[0] + " " + fields[1] + " redlaunch-ssh-key:1\n"
	if string(contents) != expected {
		t.Fatalf("authorized_keys = %q, want %q", string(contents), expected)
	}
	if info, err := os.Stat(authorizedKeys); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys mode = %o, want 600", info.Mode().Perm())
	}

	stored, err := repository.GetServerSSHKey(t.Context(), setup.Key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PublicKey != setup.Key.PublicKey {
		t.Fatalf("stored public key = %q, want %q", stored.PublicKey, setup.Key.PublicKey)
	}
}

func TestServerSSHKeyCreatePreservesUnmanagedKeys(t *testing.T) {
	directory := t.TempDir()
	authorizedKeys := filepath.Join(directory, "authorized_keys")
	unmanaged := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMunmanaged admin@laptop\n# a comment\n"
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authorizedKeys, []byte(unmanaged), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := newFakeServerSSHKeyRepository()
	service, err := NewServerSSHKeyService(authorizedKeys, "deploy", repository, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := service.Create(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	body := string(contents)
	if !strings.Contains(body, "AAAAC3NzaC1lZDI1NTE5AAAAIOMunmanaged") || !strings.Contains(body, "# a comment") {
		t.Fatalf("unmanaged lines were not preserved: %q", body)
	}
	if !strings.Contains(body, "redlaunch-ssh-key:1") || !strings.Contains(body, "redlaunch-ssh-key:2") {
		t.Fatalf("managed keys were not installed: %q", body)
	}
	if strings.Count(body, "redlaunch-ssh-key:") != 2 {
		t.Fatalf("managed key count is wrong: %q", body)
	}

	if err := service.Revoke(t.Context(), setup.Key.ID); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	body = string(contents)
	if strings.Contains(body, "redlaunch-ssh-key:1") {
		t.Fatalf("revoked key was not removed: %q", body)
	}
	if !strings.Contains(body, "redlaunch-ssh-key:2") || !strings.Contains(body, "AAAAC3NzaC1lZDI1NTE5AAAAIOMunmanaged") {
		t.Fatalf("remaining keys were not preserved after revoke: %q", body)
	}
	if _, err := repository.GetServerSSHKey(t.Context(), second.Key.ID); err != nil {
		t.Fatalf("remaining key metadata was removed: %v", err)
	}
}

func TestServerSSHKeyCreateRejectsDisplayName(t *testing.T) {
	authorizedKeys := filepath.Join(t.TempDir(), "authorized_keys")
	service, err := NewServerSSHKeyService(authorizedKeys, "deploy", newFakeServerSSHKeyRepository(), &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(t.Context(), "   "); !errors.Is(err, application.ErrSSHKeyDisplayNameRequired) {
		t.Fatalf("empty display name error = %v, want ErrSSHKeyDisplayNameRequired", err)
	}
	if _, err := os.Stat(authorizedKeys); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authorized_keys was created for invalid input: %v", err)
	}
}

func TestServerSSHKeyRequiresAbsolutePath(t *testing.T) {
	if _, err := NewServerSSHKeyService("relative/authorized_keys", "deploy", newFakeServerSSHKeyRepository(), nil); err == nil {
		t.Fatal("relative authorized_keys path was accepted")
	}
	if _, err := NewServerSSHKeyService("/", "deploy", newFakeServerSSHKeyRepository(), nil); err == nil {
		t.Fatal("filesystem-root authorized_keys path was accepted")
	}
}

func TestServerSSHKeyRefusesSymlinkedAuthorizedKeys(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "real_keys")
	if err := os.WriteFile(target, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMx other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "authorized_keys")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	service, err := NewServerSSHKeyService(link, "deploy", newFakeServerSSHKeyRepository(), &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(t.Context(), "symlink"); err == nil {
		t.Fatal("symlinked authorized_keys was accepted")
	}
}
