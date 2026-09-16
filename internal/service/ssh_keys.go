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

// ServerSSHKeyServiceCatalog is the managed-application capability used to
// resolve a service restriction to a host forwarding target.
type ServerSSHKeyServiceCatalog interface {
	Get(context.Context, int64) (application.Application, error)
	ListServices(context.Context, int64) ([]application.Service, error)
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
	projectsRoot       string
	repository         ServerSSHKeyRepository
	applications       ServerSSHKeyServiceCatalog
	keyGenerator       SSHKeyGenerator

	mu sync.Mutex
}

// NewServerSSHKeyService constructs the host SSH key service for the
// dedicated redlaunch user.
func NewServerSSHKeyService(projectsRoot string, repository ServerSSHKeyRepository, applications ServerSSHKeyServiceCatalog, keyGenerator SSHKeyGenerator) (*ServerSSHKeyService, error) {
	return NewServerSSHKeyServiceWithPath(ServerSSHAuthorizedKeysPath, ServerSSHUsername, projectsRoot, repository, applications, keyGenerator)
}

// NewServerSSHKeyServiceWithPath constructs the service with an explicit
// authorized_keys location. Production code uses NewServerSSHKeyService; tests
// use this to point at temporary files.
func NewServerSSHKeyServiceWithPath(authorizedKeysPath, username, projectsRoot string, repository ServerSSHKeyRepository, applications ServerSSHKeyServiceCatalog, keyGenerator SSHKeyGenerator) (*ServerSSHKeyService, error) {
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
	if strings.TrimSpace(projectsRoot) == "" {
		return nil, errors.New("projects root must not be empty")
	}
	root, err := filepath.Abs(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve projects root: %w", err)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return nil, errors.New("projects root must not be the filesystem root")
	}
	if repository == nil || applications == nil {
		return nil, errors.New("SSH key service dependencies are incomplete")
	}
	if keyGenerator == nil {
		keyGenerator = CommandSSHKeyGenerator{}
	}
	return &ServerSSHKeyService{
		authorizedKeysPath: path,
		username:           name,
		projectsRoot:       root,
		repository:         repository,
		applications:       applications,
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
// An input without a service restriction creates a full shell key; otherwise
// the key is tunnel-only and OpenSSH limits forwarding to the resolved
// service target.
func (s *ServerSSHKeyService) Create(ctx context.Context, input application.ServerSSHKeyInput) (application.ServerSSHKeySetup, error) {
	if s == nil || s.repository == nil || s.applications == nil {
		return application.ServerSSHKeySetup{}, errors.New("SSH key service is not configured")
	}
	normalized, err := application.ValidateSSHKeyDisplayName(input.DisplayName)
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	permitOpen, applicationID, serviceName, err := s.resolveRestriction(ctx, input)
	if err != nil {
		return application.ServerSSHKeySetup{}, err
	}
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
		ApplicationID:  applicationID,
		ServiceName:    serviceName,
		PermitOpen:     permitOpen,
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

// resolveRestriction validates an optional service restriction and returns
// the enforced forwarding target with the referenced service. An input
// without a restriction resolves to empty values and an unrestricted key.
func (s *ServerSSHKeyService) resolveRestriction(ctx context.Context, input application.ServerSSHKeyInput) (permitOpen string, applicationID int64, serviceName string, err error) {
	serviceName = strings.TrimSpace(input.ServiceName)
	if input.ApplicationID == 0 && serviceName == "" {
		return "", 0, "", nil
	}
	if input.ApplicationID < 1 || serviceName == "" {
		return "", 0, "", application.ErrSSHKeyServiceRequired
	}
	serviceName, err = application.ValidateServiceName(serviceName)
	if err != nil {
		return "", 0, "", application.ErrSSHKeyServiceNotFound
	}
	item, err := s.applications.Get(ctx, input.ApplicationID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			return "", 0, "", application.ErrSSHKeyServiceNotFound
		}
		return "", 0, "", err
	}
	services, err := s.applications.ListServices(ctx, input.ApplicationID)
	if err != nil {
		return "", 0, "", err
	}
	var target *application.Service
	for index := range services {
		if services[index].Name == serviceName {
			target = &services[index]
			break
		}
	}
	if target == nil {
		return "", 0, "", application.ErrSSHKeyServiceNotFound
	}
	// Redis services publish their persisted port on loopback, so the target
	// needs no Compose file access.
	if target.Type == application.ServiceTypeRedis || strings.TrimSpace(target.RedisPort) != "" {
		port := strings.TrimSpace(target.RedisPort)
		if !validApplicationPort(port) {
			return "", 0, "", fmt.Errorf("stored Redis port for service %q is invalid", serviceName)
		}
		return "127.0.0.1:" + port, item.ID, serviceName, nil
	}
	port, err := s.resolveServiceHostPort(ctx, item, serviceName)
	if err != nil {
		return "", 0, "", err
	}
	return "127.0.0.1:" + port, item.ID, serviceName, nil
}

// resolveServiceHostPort returns the first TCP host port published by one
// managed service, canonicalized for a 127.0.0.1 target. Host sshd resolves
// permitopen targets from the host network namespace, so only published host
// ports qualify; container-private ports (notably Postgres) cannot be tunnel
// targets. Redlaunch generates loopback or all-interface publishes, both
// reachable via 127.0.0.1; a service bound to one specific public IP is out
// of scope and its tunnel fails closed with connection refused.
func (s *ServerSSHKeyService) resolveServiceHostPort(ctx context.Context, item application.Application, serviceName string) (string, error) {
	directory, err := s.managedSSHKeyApplicationDirectory(item)
	if err != nil {
		return "", err
	}
	composePath, err := findApplicationComposeFile(directory)
	if err != nil {
		return "", fmt.Errorf("find application Compose file: %w", err)
	}
	if composePath == "" {
		return "", fmt.Errorf("application Compose file does not exist for %q", item.Name)
	}
	snapshot, err := snapshotManagedFile(composePath)
	if err != nil {
		return "", fmt.Errorf("read application Compose file: %w", err)
	}
	if !snapshot.exists {
		return "", fmt.Errorf("application Compose file does not exist for %q", item.Name)
	}
	config, err := parseApplicationServiceBlock(string(snapshot.contents), serviceName)
	if err != nil {
		if errors.Is(err, application.ErrServiceNotFound) {
			return "", application.ErrSSHKeyServiceNotFound
		}
		return "", err
	}
	for _, mapping := range config.PortMappings {
		if strings.ToLower(strings.TrimSpace(mapping.Protocol)) != "tcp" {
			continue
		}
		// Port mappings are operator-controlled Compose text: re-validate
		// the host port instead of trusting the parsed value.
		hostPort := strings.TrimSpace(mapping.HostPort)
		if !validApplicationPort(hostPort) {
			continue
		}
		return hostPort, nil
	}
	return "", application.ErrSSHKeyServiceHasNoTargetPort
}

// managedSSHKeyApplicationDirectory locates one application's managed
// directory with the same containment guarantees as the application service:
// the stored folder name is validated and the result must stay inside the
// managed applications directory without symlink escapes.
func (s *ServerSSHKeyService) managedSSHKeyApplicationDirectory(item application.Application) (string, error) {
	folderName, err := application.ValidateFolderName(item.FolderName)
	if err != nil {
		return "", fmt.Errorf("validate stored application folder: %w", err)
	}
	root := filepath.Join(s.projectsRoot, applicationsDir)
	directory := filepath.Join(root, folderName)
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("application directory is outside the managed applications directory")
	}
	if err := checkManagedAncestors(root, directory); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", application.ErrSSHKeyServiceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("inspect application directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("application directory must not be a symlink")
	}
	if !info.IsDir() {
		return "", errors.New("application path is not a directory")
	}
	return directory, nil
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

// serverSSHKeyTunnelOptions hardens restricted keys to tunnel-only use,
// mirroring the GitHub Actions gateway convention: no PTY, agent, X11, or
// user-rc processing, with forwarding re-enabled for exactly one target.
const serverSSHKeyTunnelOptions = `no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc`

func serverSSHKeyLine(key application.ServerSSHKey) (string, error) {
	fields := strings.Fields(key.PublicKey)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", fmt.Errorf("invalid public key for SSH key %d", key.ID)
	}
	base := fields[0] + " " + fields[1] + " " + serverSSHKeyComment(key.ID)
	if strings.TrimSpace(key.PermitOpen) == "" {
		return base, nil
	}
	// Fail closed on stored targets that no longer validate: never render a
	// restricted key as an unrestricted line.
	target, err := application.ValidateSSHKeyPermitOpen(key.PermitOpen)
	if err != nil {
		return "", fmt.Errorf("invalid forwarding target for SSH key %d: %w", key.ID, err)
	}
	return serverSSHKeyTunnelOptions + `,permitopen="` + target + `" ` + base, nil
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
