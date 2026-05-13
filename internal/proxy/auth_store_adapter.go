package proxy

import (
	"context"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type CoreManagerAuthStore struct {
	manager *coreauth.Manager
	store   coreauth.Store
}

func NewCoreManagerAuthStore(manager *coreauth.Manager, store coreauth.Store) *CoreManagerAuthStore {
	return &CoreManagerAuthStore{
		manager: manager,
		store:   store,
	}
}

func (s *CoreManagerAuthStore) ListAuths() []*coreauth.Auth {
	if s.manager == nil {
		return nil
	}
	return s.manager.List()
}

func (s *CoreManagerAuthStore) UpdateAuth(a *coreauth.Auth) error {
	if s.store == nil {
		return nil
	}
	_, err := s.store.Save(context.Background(), a)
	return err
}
