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

type fakeSSHKeyApplications struct {
	applications map[int64]application.Application
	services     map[int64][]application.Service
}

func (f *fakeSSHKeyApplications) Get(_ context.Context, id int64) (application.Application, error) {
	item, ok := f.applications[id]
	if !ok {
		return application.Application{}, application.ErrNotFound
	}
	return item, nil
}

func (f *fakeSSHKeyApplications) ListServices(_ context.Context, id int64) ([]application.Service, error) {
	return append([]application.Service(nil), f.services[id]...), nil
}

func newSSHKeyTestService(t *testing.T, authorizedKeys string, repository *fakeServerSSHKeyRepository, applications *fakeSSHKeyApplications) *ServerSSHKeyService {
	t.Helper()
	if applications == nil {
		applications = &fakeSSHKeyApplications{}
	}
	service, err := NewServerSSHKeyServiceWithPath(authorizedKeys, "redlaunch", t.TempDir(), repository, applications, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestServerSSHKeyCreateInstallsPublicKey(t *testing.T) {
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	repository := newFakeServerSSHKeyRepository()
	service := newSSHKeyTestService(t, authorizedKeys, repository, nil)

	setup, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "GHA migrator workflow access"})
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
	if stored.PermitOpen != "" || stored.ApplicationID != 0 || stored.ServiceName != "" {
		t.Fatalf("unrestricted key stored restriction = %+v, want none", stored)
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
	service := newSSHKeyTestService(t, authorizedKeys, repository, nil)
	setup, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "second"})
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
	service := newSSHKeyTestService(t, authorizedKeys, newFakeServerSSHKeyRepository(), nil)
	if _, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "   "}); !errors.Is(err, application.ErrSSHKeyDisplayNameRequired) {
		t.Fatalf("empty display name error = %v, want ErrSSHKeyDisplayNameRequired", err)
	}
	if _, err := os.Stat(authorizedKeys); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authorized_keys was created for invalid input: %v", err)
	}
}

func TestServerSSHKeyUsesDedicatedUser(t *testing.T) {
	service, err := NewServerSSHKeyService(t.TempDir(), newFakeServerSSHKeyRepository(), &fakeSSHKeyApplications{}, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	if service.Username() != "redlaunch" {
		t.Fatalf("SSH username = %q, want redlaunch", service.Username())
	}
	if service.AuthorizedKeysPath() != "/home/redlaunch/.ssh/authorized_keys" {
		t.Fatalf("authorized keys path = %q, want the redlaunch home", service.AuthorizedKeysPath())
	}
}

func TestServerSSHKeyRequiresAbsolutePath(t *testing.T) {
	if _, err := NewServerSSHKeyServiceWithPath("relative/authorized_keys", "redlaunch", t.TempDir(), newFakeServerSSHKeyRepository(), &fakeSSHKeyApplications{}, nil); err == nil {
		t.Fatal("relative authorized_keys path was accepted")
	}
	if _, err := NewServerSSHKeyServiceWithPath("/", "redlaunch", t.TempDir(), newFakeServerSSHKeyRepository(), &fakeSSHKeyApplications{}, nil); err == nil {
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
	service := newSSHKeyTestService(t, link, newFakeServerSSHKeyRepository(), nil)
	if _, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "symlink"}); err == nil {
		t.Fatal("symlinked authorized_keys was accepted")
	}
}

func TestServerSSHKeyRestrictsRedisService(t *testing.T) {
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	repository := newFakeServerSSHKeyRepository()
	applications := &fakeSSHKeyApplications{
		applications: map[int64]application.Application{
			7: {ID: 7, Name: "Status page", FolderName: "status-page"},
		},
		services: map[int64][]application.Service{
			7: {{ID: 9, ApplicationID: 7, Name: "cache", Type: application.ServiceTypeRedis, RedisPort: "6379"}},
		},
	}
	service := newSSHKeyTestService(t, authorizedKeys, repository, applications)

	setup, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "cache tunnel", ApplicationID: 7, ServiceName: "cache"})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Key.PermitOpen != "127.0.0.1:6379" || setup.Key.ApplicationID != 7 || setup.Key.ServiceName != "cache" {
		t.Fatalf("restricted key = %+v, want Redis target", setup.Key)
	}
	contents, err := os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(setup.Key.PublicKey)
	expected := `no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc,permitopen="127.0.0.1:6379" ` + fields[0] + " " + fields[1] + " redlaunch-ssh-key:1\n"
	if string(contents) != expected {
		t.Fatalf("authorized_keys = %q, want %q", string(contents), expected)
	}
}

func TestServerSSHKeyRestrictsComposeServicePort(t *testing.T) {
	root := t.TempDir()
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	directory := filepath.Join(root, "applications", "status-page")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  web:\n    image: nginx:1.27\n    ports:\n      - \"8080:80\"\n  worker:\n    image: busybox:1.36\n"
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := newFakeServerSSHKeyRepository()
	applications := &fakeSSHKeyApplications{
		applications: map[int64]application.Application{
			7: {ID: 7, Name: "Status page", FolderName: "status-page"},
		},
		services: map[int64][]application.Service{
			7: {{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
		},
	}
	service, err := NewServerSSHKeyServiceWithPath(authorizedKeys, "redlaunch", root, repository, applications, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}

	setup, err := service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "web tunnel", ApplicationID: 7, ServiceName: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Key.PermitOpen != "127.0.0.1:8080" {
		t.Fatalf("restricted target = %q, want 127.0.0.1:8080", setup.Key.PermitOpen)
	}
	contents, err := os.ReadFile(authorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `permitopen="127.0.0.1:8080"`) {
		t.Fatalf("authorized_keys missing restriction: %q", string(contents))
	}
}

func TestServerSSHKeyRejectsRestrictionWithoutPublishedPort(t *testing.T) {
	root := t.TempDir()
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	directory := filepath.Join(root, "applications", "status-page")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  db:\n    image: postgres:17\n"
	if err := os.WriteFile(filepath.Join(directory, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := newFakeServerSSHKeyRepository()
	applications := &fakeSSHKeyApplications{
		applications: map[int64]application.Application{
			7: {ID: 7, Name: "Status page", FolderName: "status-page"},
		},
		services: map[int64][]application.Service{
			7: {{ID: 9, ApplicationID: 7, Name: "db", Type: application.ServiceTypePostgreSQL}},
		},
	}
	service, err := NewServerSSHKeyServiceWithPath(authorizedKeys, "redlaunch", root, repository, applications, &fakeServerSSHKeyGenerator{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Create(t.Context(), application.ServerSSHKeyInput{DisplayName: "db tunnel", ApplicationID: 7, ServiceName: "db"})
	if !errors.Is(err, application.ErrSSHKeyServiceHasNoTargetPort) {
		t.Fatalf("unpublished service error = %v, want ErrSSHKeyServiceHasNoTargetPort", err)
	}
	if _, statErr := os.Stat(authorizedKeys); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("authorized_keys was written for a rejected restriction")
	}
	if keys, err := repository.ListServerSSHKeys(t.Context()); err != nil || len(keys) != 0 {
		t.Fatalf("rejected restriction stored metadata: keys=%v err=%v", keys, err)
	}
}

func TestServerSSHKeyRejectsUnknownRestrictionService(t *testing.T) {
	authorizedKeys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	applications := &fakeSSHKeyApplications{
		applications: map[int64]application.Application{
			7: {ID: 7, Name: "Status page", FolderName: "status-page"},
		},
		services: map[int64][]application.Service{
			7: {{ID: 9, ApplicationID: 7, Name: "web", Type: application.ServiceTypeApplication}},
		},
	}
	service := newSSHKeyTestService(t, authorizedKeys, newFakeServerSSHKeyRepository(), applications)

	for _, testCase := range []struct {
		name    string
		input   application.ServerSSHKeyInput
		wantErr error
	}{
		{name: "unknown application", input: application.ServerSSHKeyInput{DisplayName: "x", ApplicationID: 99, ServiceName: "web"}, wantErr: application.ErrSSHKeyServiceNotFound},
		{name: "unknown service", input: application.ServerSSHKeyInput{DisplayName: "x", ApplicationID: 7, ServiceName: "missing"}, wantErr: application.ErrSSHKeyServiceNotFound},
		{name: "missing service", input: application.ServerSSHKeyInput{DisplayName: "x", ApplicationID: 7}, wantErr: application.ErrSSHKeyServiceRequired},
		{name: "missing application", input: application.ServerSSHKeyInput{DisplayName: "x", ServiceName: "web"}, wantErr: application.ErrSSHKeyServiceRequired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.Create(t.Context(), testCase.input)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("input %+v error = %v, want %v", testCase.input, err, testCase.wantErr)
			}
		})
	}
}
