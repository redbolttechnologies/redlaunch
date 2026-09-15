package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"redlaunch/internal/application"
)

// ServerSSHKeyRepository is the persistence capability required by the
// operator-managed host SSH key use case.
type ServerSSHKeyRepository interface {
	ListServerSSHKeys(context.Context) ([]application.ServerSSHKey, error)
	GetServerSSHKey(context.Context, int64) (application.ServerSSHKey, error)
	CreateServerSSHKey(context.Context, application.ServerSSHKey) (application.ServerSSHKey, error)
	DeleteServerSSHKey(context.Context, int64) error
}

// ServerSSHUsername is the dedicated login user for the Settings SSH keys
// tab. The user is created by scripts/setup.sh with password login locked;
// only keys installed through this service grant access.
const ServerSSHUsername = "redlaunch"

// ServerSSHAuthorizedKeysPath is the host authorized_keys file for the
// dedicated SSH user. Docker Compose mounts this exact path into the manager
// container so the service can install and revoke keys.
const ServerSSHAuthorizedKeysPath = "/home/redlaunch/.ssh/authorized_keys"

// ServerSSHKeyService manages Ed25519 keys for the dedicated redlaunch host
// user. Public keys are persisted in SQLite and mirrored into the host's
// authorized_keys file; private keys are returned transiently at creation.
type ServerSSHKeyService struct {
	authorizedKeysPath string
	username           string
	repository         ServerSSHKeyRepository
	keyGenerator       SSHKeyGenerator

	mu sync.Mutex
}

// NewServerSSHKeyService constructs the host SSH key service for the
// dedicated redlaunch user.
func NewServerSSHKeyService(repository ServerSSHKeyRepository, keyGenerator SSHKeyGenerator) (*ServerSSHKeyService, error) {
	return NewServerSSHKeyServiceWithPath(ServerSSHAuthorizedKeysPath, ServerSSHUsername, repository, keyGenerator)
}

// NewServerSSHKeyServiceWithPath constructs the service with an explicit
// authorized_keys location. Production code uses NewServerSSHKeyService; tests
// use this to point at temporary files.
func NewServerSSHKeyServiceWithPath(authorizedKeysPath, username string, repository ServerSSHKeyRepository, keyGenerator SSHKeyGenerator) (*ServerSSHKeyService, error) {
	path := strings.TrimSpace(authorizedKeysPath)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("SSH authorized keys path must be absolute")
	}
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return nil, errors.New("SSH authorized keys path must not be the filesystem root")
	}
	path = cleaned
	name := strings.TrimSpace(username)
	if name == "" {
		return nil, errors.New("SSH username is required")
	}
	if repository == nil {
		return nil, errors.New("SSH key service repository is required")
	}
	if keyGenerator == nil {
		keyGenerator = CommandSSHKeyGenerator{}
	}
	return &ServerSSHKeyService{
		authorizedKeysPath: path,
		username:           name,
		repository:         repository,
		keyGenerator:       keyGenerator,
	}, nil
}

// Username returns the OS login name these keys grant access to.
func (s *ServerSSHKeyService) Username() string {
	if s == nil || strings.TrimSpace(s.username) == "" {
		return ServerSSHUsername
	}
	return s.username
}

// AuthorizedKeysPath returns the configured authorized_keys location.
func (s *ServerSSHKeyService) AuthorizedKeysPath() string {
	if s == nil {
		return ""
	}
	return s.authorizedKeysPath
}

// List returns operator-managed host keys in creation order.
func (s *ServerSSHKeyService) List(ctx context.Context) ([]application.ServerSSHKey, error) {
	return s.repository.ListServerSSHKeys(ctx)
}

// Create generates an Ed25519 pair, persists the public half, and installs it
// in authorized_keys. The private half is returned once and never stored.
func (s *ServerSSHKeyService) Create(ctx context.Context, displayName string) (application.ServerSSHKeySetup, error) {
	if s == nil || s.repository == nil {
		return application.ServerSSHKeySetup{}, errors.New("SSH key service is not configured")
	}
	normalized, err := application.ValidateSSHKeyDisplayName(displayName)
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	privateKey, publicKey, err := s.keyGenerator.Generate(ctx, "redlaunch-ssh-key")
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}
	fingerprint, err := sshKeyFingerprint(publicKey)
	if err != nil {
		return application.ServerSSHKeySetup{}, fmt.Errorf("fingerprint generated SSH key: %w", err)
	}
	now := time.Now().UTC()
	saved, err := s.repository.CreateServerSSHKey(ctx, application.ServerSSHKey{
		DisplayName:    normalized,
		PublicKey:      strings.TrimSpace(publicKey),
		KeyFingerprint: fingerprint,
		CreatedAt:      now,
	})
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}
	keys, err := s.repository.ListServerSSHKeys(ctx)
	if err != nil {
		_ = s.repository.DeleteServerSSHKey(context.Background(), saved.ID)
		return application.ServerSSHKeySetup{}, err
	}
	if err := writeServerSSHKeysFile(s.authorizedKeysPath, keys); err != nil {
		_ = s.repository.DeleteServerSSHKey(context.Background(), saved.ID)
		return application.ServerSSHKeySetup{}, err
	}
	return application.ServerSSHKeySetup{Key: saved, PrivateKey: privateKey}, nil
}

// Revoke removes one key from SQLite and from authorized_keys.
func (s *ServerSSHKeyService) Revoke(ctx context.Context, id int64) error {
	if s == nil || s.repository == nil {
		return errors.New("SSH key service is not configured")
	}
	if id < 1 {
		return application.ErrSSHKeyNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.repository.GetServerSSHKey(ctx, id)
	if err != nil {
		return err
	}
	keys, err := s.repository.ListServerSSHKeys(ctx)
	if err != nil {
		return err
	}
	remaining := make([]application.ServerSSHKey, 0, len(keys))
	for _, key := range keys {
		if key.ID != id {
			remaining = append(remaining, key)
		}
	}
	if err := writeServerSSHKeysFile(s.authorizedKeysPath, remaining); err != nil {
		return err
	}
	if err := s.repository.DeleteServerSSHKey(ctx, existing.ID); err != nil {
		_ = writeServerSSHKeysFile(s.authorizedKeysPath, keys)
		return err
	}
	return nil
}

func serverSSHKeyComment(id int64) string {
	return fmt.Sprintf("redlaunch-ssh-key:%d", id)
}

func isServerSSHKeyManagedLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	if !strings.HasPrefix(last, "redlaunch-ssh-key:") {
		return false
	}
	suffix := strings.TrimPrefix(last, "redlaunch-ssh-key:")
	if suffix == "" {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func serverSSHKeyLine(key application.ServerSSHKey) (string, error) {
	fields := strings.Fields(key.PublicKey)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", fmt.Errorf("invalid public key for SSH key %d", key.ID)
	}
	return fields[0] + " " + fields[1] + " " + serverSSHKeyComment(key.ID), nil
}

func writeServerSSHKeysFile(path string, keys []application.ServerSSHKey) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("SSH authorized keys path is not configured")
	}
	if !filepath.IsAbs(path) {
		return errors.New("SSH authorized keys path must be absolute")
	}
	directory := filepath.Dir(filepath.Clean(path))
	if err := ensureSSHKeysDirectory(directory); err != nil {
		return err
	}

	var preserved []string
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SSH authorized keys must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return errors.New("SSH authorized keys is not a regular file")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read SSH authorized keys: %w", err)
		}
		for _, line := range strings.Split(string(contents), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if isServerSSHKeyManagedLine(line) {
				continue
			}
			preserved = append(preserved, line)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect SSH authorized keys: %w", err)
	}

	sorted := append([]application.ServerSSHKey(nil), keys...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].ID < sorted[right].ID })
	lines := append([]string(nil), preserved...)
	for _, key := range sorted {
		line, err := serverSSHKeyLine(key)
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}
	contents := ""
	if len(lines) > 0 {
		contents = strings.Join(lines, "\n") + "\n"
	}
	if err := writeManagedFileInPlace(path, contents, 0o600); err != nil {
		return fmt.Errorf("write SSH authorized keys: %w", err)
	}
	return nil
}

func ensureSSHKeysDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create SSH directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SSH directory must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("SSH path parent is not a directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("set SSH directory permissions: %w", err)
	}
	return nil
}
