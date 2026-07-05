package configaccess

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func Register(cfg *sdkconfig.SDKConfig) {
	if cfg == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	keyConfigs := buildKeyConfigMap(cfg)
	if len(keyConfigs) == 0 {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeConfigAPIKey,
		newProvider(sdkaccess.DefaultAccessProviderName, keyConfigs),
	)
}

func buildKeyConfigMap(cfg *sdkconfig.SDKConfig) map[string]keyConfig {
	result := make(map[string]keyConfig)

	rows := usage.ListAPIKeys()
	profiles := usage.ListAPIKeyPermissionProfiles()
	for _, row := range rows {
		row = usage.EffectiveAPIKeyRowWithProfiles(row, profiles)
		trimmed := strings.TrimSpace(row.Key)
		if trimmed == "" || row.Disabled {
			continue
		}
		if _, exists := result[trimmed]; exists {
			continue
		}
		result[trimmed] = keyConfig{
			allowedModels:        row.AllowedModels,
			allowedChannels:      row.AllowedChannels,
			allowedChannelGroups: row.AllowedChannelGroups,
			dailyLimit:           row.DailyLimit,
			totalQuota:           row.TotalQuota,
			spendingLimit:        row.SpendingLimit,
			monthlySpendingLimit: row.MonthlySpendingLimit,
			billingCycleAnchor:   row.BillingCycleAnchor,
			concurrencyLimit:     row.ConcurrencyLimit,
			rpmLimit:             row.RPMLimit,
			tpmLimit:             row.TPMLimit,
			systemPrompt:         row.SystemPrompt,
		}
	}

	for _, k := range cfg.APIKeys {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		if _, exists := result[trimmed]; exists {
			continue
		}
		result[trimmed] = keyConfig{}
	}

	return result
}

type keyConfig struct {
	allowedModels        []string
	allowedChannels      []string
	allowedChannelGroups []string
	dailyLimit           int
	totalQuota           int
	spendingLimit        float64
	monthlySpendingLimit float64
	billingCycleAnchor   string
	concurrencyLimit     int
	rpmLimit             int
	tpmLimit             int
	systemPrompt         string
}

type provider struct {
	name string
	keys map[string]keyConfig
}

func newProvider(name string, keyConfigs map[string]keyConfig) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}
	return &provider{name: providerName, keys: keyConfigs}
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkaccess.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if len(p.keys) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}

	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	apiKey := extractBearerToken(authHeader)

	candidates := []struct {
		value  string
		source string
	}{
		{apiKey, "authorization"},
		{authHeaderGoogle, "x-goog-api-key"},
		{authHeaderAnthropic, "x-api-key"},
		{queryKey, "query-key"},
		{queryAuthToken, "query-auth-token"},
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		if kc, ok := p.keys[candidate.value]; ok {
			metadata := map[string]string{
				"source": candidate.source,
			}
			if len(kc.allowedModels) > 0 {
				metadata["allowed-models"] = strings.Join(kc.allowedModels, ",")
			}
			if len(kc.allowedChannels) > 0 {
				metadata["allowed-channels"] = strings.Join(kc.allowedChannels, ",")
			}
			if len(kc.allowedChannelGroups) > 0 {
				metadata["allowed-channel-groups"] = strings.Join(kc.allowedChannelGroups, ",")
			}
			if kc.dailyLimit > 0 {
				metadata["daily-limit"] = fmt.Sprintf("%d", kc.dailyLimit)
			}
			if kc.totalQuota > 0 {
				metadata["total-quota"] = fmt.Sprintf("%d", kc.totalQuota)
			}
			if kc.concurrencyLimit > 0 {
				metadata["concurrency-limit"] = fmt.Sprintf("%d", kc.concurrencyLimit)
			}
			if kc.rpmLimit > 0 {
				metadata["rpm-limit"] = fmt.Sprintf("%d", kc.rpmLimit)
			}
			if kc.tpmLimit > 0 {
				metadata["tpm-limit"] = fmt.Sprintf("%d", kc.tpmLimit)
			}
			if kc.spendingLimit > 0 {
				metadata["spending-limit"] = fmt.Sprintf("%f", kc.spendingLimit)
			}
			if kc.monthlySpendingLimit > 0 {
				metadata["monthly-spending-limit"] = fmt.Sprintf("%f", kc.monthlySpendingLimit)
			}
			if strings.TrimSpace(kc.billingCycleAnchor) != "" {
				metadata["billing-cycle-anchor"] = strings.TrimSpace(kc.billingCycleAnchor)
			}
			if kc.systemPrompt != "" {
				metadata["system-prompt"] = kc.systemPrompt
			}
			return &sdkaccess.Result{
				Provider:  p.Identifier(),
				Principal: candidate.value,
				Metadata:  metadata,
			}, nil
		}
	}

	return nil, sdkaccess.NewInvalidCredentialError()
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}
