package modelcatalog

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkmodelcatalog "github.com/router-for-me/CLIProxyAPI/v8/sdk/modelcatalog"
)

type Service struct {
	tenantID    string
	cfg         *config.Config
	authManager *coreauth.Manager
}

func New(cfg *config.Config, authManager *coreauth.Manager) *Service {
	return NewForTenant("", cfg, authManager)
}
func NewForTenant(tenantID string, cfg *config.Config, authManager *coreauth.Manager) *Service {
	if authManager != nil {
		authManager.SetModelRegistry(sdkmodelcatalog.GlobalRegistry())
	}
	return &Service{tenantID: tenantID, cfg: cfg, authManager: authManager}
}
