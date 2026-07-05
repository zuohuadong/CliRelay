package cliproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type serviceTestPluginExecutor struct{}

func (serviceTestPluginExecutor) Identifier() string {
	return "plugin-provider"
}

func (serviceTestPluginExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestPluginExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (serviceTestPluginExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (serviceTestPluginExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestPluginExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRegisterAvailableExecutors(t *testing.T) {
	oldRegisterPluginExecutors := registerPluginExecutors
	pluginRegisterCalls := 0
	var expectedPluginHost *pluginhost.Host
	var expectedManager *coreauth.Manager
	registerPluginExecutors = func(host *pluginhost.Host, manager *coreauth.Manager) {
		pluginRegisterCalls++
		if host != expectedPluginHost {
			t.Fatalf("plugin executor registration host = %p, want %p", host, expectedPluginHost)
		}
		if manager != expectedManager {
			t.Fatalf("plugin executor registration manager = %p, want %p", manager, expectedManager)
		}
		manager.RegisterExecutor(serviceTestPluginExecutor{})
	}
	t.Cleanup(func() {
		registerPluginExecutors = oldRegisterPluginExecutors
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	expectedPluginHost = service.pluginHost
	expectedManager = service.coreManager
	service.ensureWebsocketGateway()

	service.registerAvailableExecutors(nil, executorRegistrationOptions{
		includeBaseline: true,
		includePlugins:  true,
	})

	if pluginRegisterCalls != 1 {
		t.Fatalf("plugin executor registration calls = %d, want 1", pluginRegisterCalls)
	}

	providers := []string{
		"codex",
		"claude",
		"gemini",
		"vertex",
		"aistudio",
		"antigravity",
		"kimi",
		"xai",
		"agnes",
		"openai-compatibility",
		"plugin-provider",
	}
	for _, provider := range providers {
		resolved, ok := service.coreManager.Executor(provider)
		if !ok || resolved == nil {
			t.Fatalf("expected executor for provider %s after registration", provider)
		}
	}

	resolved, _ := service.coreManager.Executor("plugin-provider")
	if _, isPlugin := resolved.(serviceTestPluginExecutor); !isPlugin {
		t.Fatalf("executor type = %T, want serviceTestPluginExecutor", resolved)
	}
}

func TestRegisterExecutorForAuth_OpenAICompatUsesNamespacedProviderKey(t *testing.T) {
	testCases := []struct {
		name  string
		auths []*coreauth.Auth
	}{
		{
			name: "native first",
			auths: []*coreauth.Auth{
				{ID: "native-kimi", Provider: "kimi"},
				openAICompatKimiAuth(),
			},
		},
		{
			name: "compat first",
			auths: []*coreauth.Auth{
				openAICompatKimiAuth(),
				{ID: "native-kimi", Provider: "kimi"},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{
				cfg:         &config.Config{},
				coreManager: coreauth.NewManager(nil, nil, nil),
			}

			service.registerExecutorsForAuths(tt.auths, true)

			nativeExecutor, okNative := service.coreManager.Executor("kimi")
			if !okNative {
				t.Fatal("expected native kimi executor")
			}
			if _, okKimi := nativeExecutor.(*runtimeexecutor.KimiExecutor); !okKimi {
				t.Fatalf("native executor type = %T, want *executor.KimiExecutor", nativeExecutor)
			}

			compatExecutor, okCompat := service.coreManager.Executor("openai-compatible-kimi")
			if !okCompat {
				t.Fatal("expected namespaced OpenAI-compatible executor")
			}
			if _, okOpenAICompat := compatExecutor.(*runtimeexecutor.OpenAICompatExecutor); !okOpenAICompat {
				t.Fatalf("compat executor type = %T, want *executor.OpenAICompatExecutor", compatExecutor)
			}
		})
	}
}

func TestRegisterExecutorForAuth_DedicatedOpenAICompatKeepsNativeProviderKey(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}

	service.registerExecutorsForAuths([]*coreauth.Auth{
		{
			ID:       "bigmodel-config-auth",
			Provider: "bigmodel-coding",
			Attributes: map[string]string{
				"compat_name":  "bigmodel-coding",
				"provider_key": "bigmodel-coding",
			},
		},
		{
			ID:       "astron-config-auth",
			Provider: "astron-code",
			Attributes: map[string]string{
				"compat_name":  "astron-code",
				"provider_key": "astron-code",
			},
		},
		{
			ID:       "agnes-config-auth",
			Provider: "agnes",
			Attributes: map[string]string{
				"compat_name":  "agnes",
				"provider_key": "agnes",
			},
		},
	}, true)

	bigModelExecutor, okBigModel := service.coreManager.Executor("bigmodel-coding")
	if !okBigModel {
		t.Fatal("expected native bigmodel-coding executor")
	}
	if _, ok := bigModelExecutor.(*runtimeexecutor.BigModelCodingExecutor); !ok {
		t.Fatalf("bigmodel executor type = %T, want *executor.BigModelCodingExecutor", bigModelExecutor)
	}
	if _, ok := service.coreManager.Executor("openai-compatible-bigmodel-coding"); ok {
		t.Fatal("did not expect generic openai-compatible-bigmodel-coding executor")
	}

	astronExecutor, okAstron := service.coreManager.Executor("astron-code")
	if !okAstron {
		t.Fatal("expected native astron-code executor")
	}
	if _, ok := astronExecutor.(*runtimeexecutor.AstronCodeExecutor); !ok {
		t.Fatalf("astron executor type = %T, want *executor.AstronCodeExecutor", astronExecutor)
	}
	if _, ok := service.coreManager.Executor("openai-compatible-astron-code"); ok {
		t.Fatal("did not expect generic openai-compatible-astron-code executor")
	}

	agnesExecutor, okAgnes := service.coreManager.Executor("agnes")
	if !okAgnes {
		t.Fatal("expected dedicated agnes executor")
	}
	if _, ok := agnesExecutor.(*runtimeexecutor.OpenAICompatExecutor); !ok {
		t.Fatalf("agnes executor type = %T, want *executor.OpenAICompatExecutor", agnesExecutor)
	}
	if _, ok := service.coreManager.Executor("openai-compatible-agnes"); ok {
		t.Fatal("did not expect generic openai-compatible-agnes executor")
	}
}

func openAICompatKimiAuth() *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "compat-kimi",
		Provider: "openai-compatibility",
		Label:    "kimi",
		Attributes: map[string]string{
			"compat_name":  "kimi",
			"provider_key": "kimi",
		},
	}
}
