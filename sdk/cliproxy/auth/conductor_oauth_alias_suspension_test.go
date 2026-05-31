package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type aliasRoutingExecutor struct {
	id string

	mu             sync.Mutex
	executeModels  []string
	executeAliases []string
}

func (e *aliasRoutingExecutor) Identifier() string { return e.id }

func (e *aliasRoutingExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.executeAliases = append(e.executeAliases, coreusage.RequestedModelAliasFromContext(ctx))
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *aliasRoutingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *aliasRoutingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *aliasRoutingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *aliasRoutingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *aliasRoutingExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *aliasRoutingExecutor) ExecuteAliases() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeAliases))
	copy(out, e.executeAliases)
	return out
}

func TestManagerExecute_PrefersHigherPriorityOAuthAliasProvider(t *testing.T) {
	const (
		bigModelProvider = "bigmodel-coding"
		astronProvider   = "astron-code"
		routeModel       = "glm-5.1"
		astronModel      = "astron-code-latest"
	)

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: internalconfig.DefaultBigModelCodingProviderName,
				Models: []internalconfig.OpenAICompatibilityModel{{
					Name: routeModel,
				}},
			},
			{
				Name: internalconfig.DefaultAstronCodeProviderName,
				Models: []internalconfig.OpenAICompatibilityModel{{
					Name:  astronModel,
					Alias: routeModel,
				}},
			},
		},
	})
	bigModelExecutor := &aliasRoutingExecutor{id: bigModelProvider}
	astronExecutor := &aliasRoutingExecutor{id: astronProvider}
	manager.RegisterExecutor(bigModelExecutor)
	manager.RegisterExecutor(astronExecutor)

	bigModelAuth := &Auth{
		ID:       "bigmodel-priority-auth",
		Provider: bigModelProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "bigmodel-key",
			"provider_key": bigModelProvider,
			"compat_name":  internalconfig.DefaultBigModelCodingProviderName,
			"priority":     "200",
		},
	}
	astronAuth := &Auth{
		ID:       "astron-priority-auth",
		Provider: astronProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "astron-key",
			"provider_key": astronProvider,
			"compat_name":  internalconfig.DefaultAstronCodeProviderName,
			"priority":     "300",
		},
	}
	if _, errRegister := manager.Register(context.Background(), bigModelAuth); errRegister != nil {
		t.Fatalf("register bigmodel auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), astronAuth); errRegister != nil {
		t.Fatalf("register astron auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(bigModelAuth.ID, bigModelProvider, []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(astronAuth.ID, astronProvider, []*registry.ModelInfo{{ID: routeModel}, {ID: astronModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(bigModelAuth.ID)
		reg.UnregisterClient(astronAuth.ID)
	})
	manager.RefreshSchedulerEntry(bigModelAuth.ID)
	manager.RefreshSchedulerEntry(astronAuth.ID)

	resp, errExecute := manager.Execute(context.Background(), []string{bigModelProvider, astronProvider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != astronModel {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), astronModel)
	}

	if gotModels := bigModelExecutor.ExecuteModels(); len(gotModels) != 0 {
		t.Fatalf("bigmodel execute models = %v, want none", gotModels)
	}
	gotModels := astronExecutor.ExecuteModels()
	if len(gotModels) != 1 {
		t.Fatalf("astron execute models len = %d, want 1", len(gotModels))
	}
	if gotModels[0] != astronModel {
		t.Fatalf("astron execute model = %q, want %q", gotModels[0], astronModel)
	}

	gotAliases := astronExecutor.ExecuteAliases()
	if len(gotAliases) != 1 {
		t.Fatalf("astron execute aliases len = %d, want 1", len(gotAliases))
	}
	if gotAliases[0] != routeModel {
		t.Fatalf("astron execute alias = %q, want %q", gotAliases[0], routeModel)
	}
}

func TestManagerExecute_OAuthAliasBypassesBlockedRouteModel(t *testing.T) {
	const (
		provider    = "antigravity"
		routeModel  = "claude-opus-4-6"
		targetModel = "claude-opus-4-6-thinking"
	)

	manager := NewManager(nil, nil, nil)
	executor := &aliasRoutingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:  targetModel,
			Alias: routeModel,
			Fork:  true,
		}},
	})

	auth := &Auth{
		ID:       "oauth-alias-auth",
		Provider: provider,
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			routeModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: time.Now().Add(1 * time.Hour),
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: routeModel}, {ID: targetModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != targetModel {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), targetModel)
	}

	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 {
		t.Fatalf("execute models len = %d, want 1", len(gotModels))
	}
	if gotModels[0] != targetModel {
		t.Fatalf("execute model = %q, want %q", gotModels[0], targetModel)
	}

	gotAliases := executor.ExecuteAliases()
	if len(gotAliases) != 1 {
		t.Fatalf("execute aliases len = %d, want 1", len(gotAliases))
	}
	if gotAliases[0] != routeModel {
		t.Fatalf("execute alias = %q, want %q", gotAliases[0], routeModel)
	}
}
