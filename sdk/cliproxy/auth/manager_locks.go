package auth

import "sync"

type authMutationLock struct {
	mu sync.Mutex
}

func (m *Manager) mutationLockForAuth(id string) *authMutationLock {
	lockValue, _ := m.mutationLocks.LoadOrStore(id, &authMutationLock{})
	lock, _ := lockValue.(*authMutationLock)
	if lock != nil {
		return lock
	}
	lock = &authMutationLock{}
	m.mutationLocks.Store(id, lock)
	return lock
}

func (m *Manager) refreshLockForAuth(id string) *authRefreshLock {
	lockValue, _ := m.refreshLocks.LoadOrStore(id, &authRefreshLock{})
	lock, _ := lockValue.(*authRefreshLock)
	if lock != nil {
		return lock
	}
	lock = &authRefreshLock{}
	m.refreshLocks.Store(id, lock)
	return lock
}
