package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// projectLockManager serializes work that can change one Compose project or
// its managed files. Each project gets its own admission queue, so a stalled
// Docker operation for one application does not hold up unrelated projects.
// The proxy is represented by a separate key because its configuration is
// shared by all applications.
type projectLockManager struct {
	mu    sync.Mutex
	locks map[string]*projectLock
}

type projectLock struct {
	semaphore chan struct{}
	refs      int
}

type projectLockLease struct {
	manager *projectLockManager
	key     string
	lock    *projectLock
	once    sync.Once
}

func newProjectLockManager() *projectLockManager {
	return &projectLockManager{locks: make(map[string]*projectLock)}
}

func (m *projectLockManager) acquire(ctx context.Context, key string) (*projectLockLease, error) {
	if m == nil {
		return nil, errors.New("project lock manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if key == "" {
		return nil, errors.New("project lock key is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	lock := m.locks[key]
	if lock == nil {
		lock = &projectLock{semaphore: make(chan struct{}, 1)}
		m.locks[key] = lock
	}
	lock.refs++
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		m.releaseReference(key, lock)
		return nil, err
	}

	select {
	case lock.semaphore <- struct{}{}:
		return &projectLockLease{manager: m, key: key, lock: lock}, nil
	case <-ctx.Done():
		m.releaseReference(key, lock)
		return nil, ctx.Err()
	}
}

func (l *projectLockLease) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		<-l.lock.semaphore
		l.manager.releaseReference(l.key, l.lock)
	})
}

func (m *projectLockManager) releaseReference(key string, lock *projectLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock.refs--
	if lock.refs == 0 && m.locks[key] == lock {
		delete(m.locks, key)
	}
}

const proxyProjectLockKey = "proxy"

const githubActionsGatewayProjectLockKey = "github-actions-gateway"

func applicationProjectLockKey(applicationID int64) string {
	return "application:" + formatProjectLockID(applicationID)
}

func applicationFolderLockKey(folderName string) string {
	return "application-folder:" + folderName
}

func formatProjectLockID(applicationID int64) string {
	return strconv.FormatInt(applicationID, 10)
}
