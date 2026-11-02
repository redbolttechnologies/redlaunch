package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"redlaunch/internal/application"
)

const (
	updateStateSchema       = 1
	updateStateMaxBytes     = 64 * 1024
	updateOperationType     = "release-update"
	updateHelperProjectName = "redlaunch-updater"
	updateHelperName        = "redbolt-redlaunch-updater"
)

// updateStateStore owns the durable, non-secret state for manager installation
// operations. It is intentionally a small file-based store: Redlaunch is a
// single-host application and the state must be readable by a detached helper.
type updateStateStore struct {
	projectsRoot string
	root         string
	statePath    string
	lockPath     string
	mutexPath    string
	downloads    string
}

const updateStateLockTimeout = 5 * time.Second

// updateStateMutexes serializes writers inside one process. The advisory file
// lock below serializes the manager and the detached helper, which are separate
// processes sharing the projects root.
var updateStateMutexes sync.Map

func updateStateMutex(path string) *sync.Mutex {
	value, _ := updateStateMutexes.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func newUpdateStateStore(projectsRoot string) (*updateStateStore, error) {
	root, err := filepath.Abs(strings.TrimSpace(projectsRoot))
	if err != nil || root == string(filepath.Separator) {
		return nil, errors.New("invalid projects root")
	}
	store := &updateStateStore{
		projectsRoot: root,
		root:         filepath.Join(root, "core", "updater"),
	}
	store.statePath = filepath.Join(store.root, "state.json")
	store.lockPath = filepath.Join(store.root, "operation.lock")
	store.mutexPath = filepath.Join(store.root, "state.lock")
	store.downloads = filepath.Join(store.root, "downloads")
	if err := store.ensure(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *updateStateStore) ensure() error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	for _, path := range []string{s.root, s.downloads} {
		if err := validateUpdatePathBeforeCreate(s.projectsRoot, path); err != nil {
			return err
		}
	}
	if err := ensureUpdatePrivateDirectory(s.root); err != nil {
		return err
	}
	if err := ensureUpdatePrivateDirectory(s.downloads); err != nil {
		return err
	}
	for _, path := range []string{s.root, s.downloads} {
		if err := checkUpdatePathContained(s.projectsRoot, path); err != nil {
			return err
		}
	}
	return nil
}

// validateUpdatePathBeforeCreate rejects paths that would escape the projects
// root before any filesystem mutation occurs. It checks lexical containment
// first and then ensures no existing ancestor in the chain is a symlink that
// would redirect creation outside the root.
func validateUpdatePathBeforeCreate(projectsRoot, candidate string) error {
	cleanRoot := filepath.Clean(projectsRoot)
	cleanCandidate := filepath.Clean(candidate)
	relative, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("update path is outside the projects root")
	}
	// Walk every existing component from the root to the candidate and reject
	// symlinks. MkdirAll would otherwise follow a symlinked parent (for
	// example PROJECTS_ROOT/core -> /tmp/evil) and create directories in the
	// target before containment is checked.
	current := cleanRoot
	info, err := os.Lstat(current)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Projects root does not exist yet (isolated tests); lexical
			// containment already establishes the candidate stays inside.
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("projects root must not be a symlink")
	}
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return errors.New("update path is outside the projects root")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// Remaining suffix does not exist yet; lexical containment already
			// establishes it stays inside the root because no existing parent
			// was a symlink.
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("update path resolves outside the projects root")
		}
	}
	return nil
}

func checkUpdatePathContained(projectsRoot, candidate string) error {
	relative, err := filepath.Rel(filepath.Clean(projectsRoot), filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("update path is outside the projects root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(projectsRoot))
	if err != nil {
		return err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return err
	}
	relative, err = filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("update path resolves outside the projects root")
	}
	return nil
}

func ensureUpdatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create update directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("update path is not a regular directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect update directory: %w", err)
	}
	return nil
}

func (s *updateStateStore) readState() (application.UpdateState, bool, error) {
	if s == nil {
		return application.UpdateState{}, false, errors.New("update state store is not configured")
	}
	info, err := os.Lstat(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return application.UpdateState{}, false, nil
	}
	if err != nil {
		return application.UpdateState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return application.UpdateState{}, false, errors.New("update state is not a regular file")
	}
	contents, err := os.ReadFile(s.statePath)
	if err != nil {
		return application.UpdateState{}, false, err
	}
	if len(contents) > updateStateMaxBytes {
		return application.UpdateState{}, false, errors.New("update state is too large")
	}
	var state application.UpdateState
	if err := json.Unmarshal(contents, &state); err != nil {
		return application.UpdateState{}, false, errors.New("update state is invalid")
	}
	if err := validateUpdateState(state); err != nil {
		return application.UpdateState{}, false, err
	}
	return state, true, nil
}

// withStoreLock serializes state and lock-file transitions. The in-process
// mutex keeps goroutines ordered while the advisory file lock provides the same
// exclusion between the manager and the detached helper. Advisory locks are
// released by the kernel when a process exits, so a crash cannot leave a stale
// lock that blocks recovery.
func (s *updateStateStore) withStoreLock(operation func() error) error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	mutex := updateStateMutex(s.mutexPath)
	mutex.Lock()
	defer mutex.Unlock()

	file, err := os.OpenFile(s.mutexPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open update state lock: %w", err)
	}
	defer file.Close()
	deadline := time.Now().Add(updateStateLockTimeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock update state: %w", err)
		}
		if time.Now().After(deadline) {
			return errors.New("update state is busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	return operation()
}

// writeState persists a transition using compare-and-swap on the operation
// revision. A caller must pass the revision it read; a stale caller is
// rejected instead of silently overwriting newer progress. On success the
// caller's revision is advanced to the persisted value.
func (s *updateStateStore) writeState(state *application.UpdateState) error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	if state == nil {
		return errors.New("update state is required")
	}
	return s.withStoreLock(func() error {
		return s.writeStateLocked(state)
	})
}

// writeStateLocked requires withStoreLock. Recovery also uses it to commit an
// uncertain transition together with acquisition of its recovery lock.
func (s *updateStateStore) writeStateLocked(state *application.UpdateState) error {
	next := *state
	if next.Schema == 0 {
		next.Schema = updateStateSchema
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}
	if len(next.Error) > 2048 {
		next.Error = next.Error[:2048]
	}
	current, ok, err := s.readState()
	if err != nil {
		return err
	}
	switch {
	case !ok:
		if next.Revision != 0 && next.Revision != 1 {
			return errors.New("update state changed concurrently")
		}
		next.Revision = 1
	case current.OperationID != next.OperationID:
		if !current.Terminal() {
			return application.ErrUpdateBusy
		}
		if next.Revision != 0 && next.Revision != 1 {
			return errors.New("update state changed concurrently")
		}
		next.Revision = 1
	default:
		if next.Revision != current.Revision {
			return errors.New("update state changed concurrently")
		}
		next.Revision = current.Revision + 1
	}
	if err := validateUpdateState(next); err != nil {
		return err
	}
	contents, err := json.Marshal(next)
	if err != nil || len(contents) > updateStateMaxBytes {
		return errors.New("update state is invalid")
	}
	if err := writeManagedFile(s.statePath, string(contents)+"\n", 0o600); err != nil {
		return err
	}
	*state = next
	return nil
}

func validateUpdateState(state application.UpdateState) error {
	if state.Schema != updateStateSchema {
		return errors.New("update state schema is unsupported")
	}
	if state.Revision < 1 || state.Revision > 1_000_000 {
		return errors.New("update revision is invalid")
	}
	if !validUpdateOperationID(state.OperationID) {
		return errors.New("update operation is invalid")
	}
	if state.Owner != "manager" && state.Owner != "helper" {
		return errors.New("update owner is invalid")
	}
	if state.Type != updateOperationType {
		return errors.New("update operation type is invalid")
	}
	switch state.Phase {
	case application.UpdatePhasePreparing,
		application.UpdatePhaseDownloading,
		application.UpdatePhaseVerified,
		application.UpdatePhaseLoaded,
		application.UpdatePhaseRestartQueued,
		application.UpdatePhaseRecreating,
		application.UpdatePhaseVerifying,
		application.UpdatePhaseComplete,
		application.UpdatePhaseFailed,
		application.UpdatePhaseUncertain:
	default:
		return errors.New("update phase is invalid")
	}
	// Enforce manager/helper ownership so concurrent writers cannot silently
	// take over another phase. Terminal phases may be written by either the
	// helper or manager reconciliation, but active phases are single-writer.
	switch state.Phase {
	case application.UpdatePhasePreparing,
		application.UpdatePhaseDownloading,
		application.UpdatePhaseVerified,
		application.UpdatePhaseLoaded:
		if state.Owner != "manager" {
			return errors.New("update owner does not own this phase")
		}
	case application.UpdatePhaseRestartQueued,
		application.UpdatePhaseRecreating,
		application.UpdatePhaseVerifying:
		if state.Owner != "helper" {
			return errors.New("update owner does not own this phase")
		}
	}
	if state.Phase == application.UpdatePhaseComplete && (state.TargetImageID == "" || state.Version == "") {
		return errors.New("completed update state is incomplete")
	}
	if state.Error != "" && len(state.Error) > 2048 {
		state.Error = state.Error[:2048]
	}
	return nil
}

func validUpdateOperationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphaNumeric && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func (s *updateStateStore) acquire(operationID, owner string) error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	if strings.TrimSpace(operationID) == "" || !validUpdateLockOwner(owner) {
		return errors.New("update operation is invalid")
	}
	return s.withStoreLock(func() error {
		return s.createLock(operationID, owner)
	})
}

func (s *updateStateStore) createLock(operationID, owner string) error {
	file, err := os.OpenFile(s.lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return application.ErrUpdateBusy
	}
	if err != nil {
		return err
	}
	if _, err := file.WriteString(operationID + "\n" + owner + "\n"); err != nil {
		file.Close()
		_ = os.Remove(s.lockPath)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(s.lockPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(s.lockPath)
		return err
	}
	return syncDirectory(s.root)
}

// requireUpdateLock reports success only when the shared lock belongs to the
// given release operation. A missing or foreign lock fails closed so a helper
// cannot mutate Docker after losing exclusion.
func (s *updateStateStore) requireUpdateLock(operationID string) error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	return s.withStoreLock(func() error {
		current, owner, ok, err := s.readLock()
		if err != nil {
			return err
		}
		if !ok || current != operationID || (owner != "manager" && owner != "helper") {
			return application.ErrUpdateBusy
		}
		return nil
	})
}

// ensureUpdateLock confirms ownership of an existing release lock or acquires
// one when no operation holds the installation. A foreign lock is never
// treated as sufficient exclusion for update recovery.
func (s *updateStateStore) ensureUpdateLock(operationID, owner string) error {
	if s == nil {
		return errors.New("update state store is not configured")
	}
	if !validUpdateOperationID(operationID) || (owner != "manager" && owner != "helper") {
		return errors.New("update operation is invalid")
	}
	return s.withStoreLock(func() error {
		current, currentOwner, ok, err := s.readLock()
		if err != nil {
			return err
		}
		if ok {
			if current == operationID && (currentOwner == "manager" || currentOwner == "helper") {
				return nil
			}
			return application.ErrUpdateBusy
		}
		return s.createLock(operationID, owner)
	})
}

// readLock returns the operation and owner recorded in the shared installation
// lock. An unreadable or malformed lock is an error so recovery fails closed
// instead of deleting an operation it does not understand.
func (s *updateStateStore) readLock() (string, string, bool, error) {
	if s == nil {
		return "", "", false, errors.New("update state store is not configured")
	}
	info, err := os.Lstat(s.lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 512 {
		return "", "", false, errors.New("update lock is invalid")
	}
	contents, err := os.ReadFile(s.lockPath)
	if err != nil {
		return "", "", false, err
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) == 0 || len(lines) > 2 {
		return "", "", false, errors.New("update lock is invalid")
	}
	operationID := strings.TrimSpace(lines[0])
	if !validUpdateOperationID(operationID) {
		return "", "", false, errors.New("update lock is invalid")
	}
	owner := ""
	if len(lines) == 2 {
		owner = strings.TrimSpace(lines[1])
		if !validUpdateLockOwner(owner) {
			return "", "", false, errors.New("update lock is invalid")
		}
	}
	return operationID, owner, true, nil
}

func validUpdateLockOwner(owner string) bool {
	switch owner {
	case "manager", "helper", "prune", "management-access":
		return true
	default:
		return false
	}
}

func (s *updateStateStore) release(operationID string) error {
	if s == nil {
		return nil
	}
	return s.withStoreLock(func() error {
		current, _, ok, err := s.readLock()
		if err != nil || !ok {
			return err
		}
		if current != operationID {
			return errors.New("update lock belongs to another operation")
		}
		if err := os.Remove(s.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(s.root)
	})
}

// removeOrphanedUpdateLock removes a release-update lock only when it is not
// owned by another installation operation. Pruning and access-mode restarts
// have their own lifecycles and must never be cleared by update recovery.
func (s *updateStateStore) removeOrphanedUpdateLock() error {
	if s == nil {
		return nil
	}
	return s.withStoreLock(func() error {
		_, owner, ok, err := s.readLock()
		if err != nil || !ok {
			return err
		}
		if owner != "manager" && owner != "helper" {
			return nil
		}
		if err := os.Remove(s.lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(s.root)
	})
}

func (s *updateStateStore) operationRunning() bool {
	if s == nil {
		return false
	}
	_, err := os.Lstat(s.lockPath)
	return err == nil
}

func (s *updateStateStore) clearState() error {
	if s == nil {
		return nil
	}
	return s.withStoreLock(func() error {
		if err := os.Remove(s.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (s *updateStateStore) helperDir() string {
	if s == nil {
		return ""
	}
	return s.root
}

func (s *updateStateStore) helperComposePath() string {
	return filepath.Join(s.helperDir(), "compose.yml")
}

func (s *updateStateStore) appOverridePath() string {
	return filepath.Join(s.helperDir(), "app-image.yml")
}

func (s *updateStateStore) downloadDir(operationID string) string {
	return filepath.Join(s.downloads, operationID)
}

func updateOperationLocked(projectsRoot string) bool {
	root := strings.TrimSpace(projectsRoot)
	if root == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(root, "core", "updater", "operation.lock"))
	return err == nil
}

func updateStateIsTerminal(state application.UpdateState) bool {
	return state.Phase == application.UpdatePhaseComplete ||
		state.Phase == application.UpdatePhaseFailed ||
		state.Phase == application.UpdatePhaseUncertain
}
