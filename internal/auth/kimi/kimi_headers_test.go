package kimi

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDeviceFlowCommonHeadersUseConfiguredIdentity(t *testing.T) {
	client := &DeviceFlowClient{
		cfg: &config.Config{KimiHeaderDefaults: config.KimiHeaderDefaults{
			UserAgent:   "custom-client",
			Platform:    "custom-platform",
			Version:     "2.0.0",
			DeviceName:  "relay-node",
			DeviceModel: "virtual",
		}},
		deviceID: "device-1",
	}

	headers := client.commonHeaders()
	want := map[string]string{
		"User-Agent":         "custom-client",
		"X-Msh-Platform":     "custom-platform",
		"X-Msh-Version":      "2.0.0",
		"X-Msh-Device-Name":  "relay-node",
		"X-Msh-Device-Model": "virtual",
		"X-Msh-Device-Id":    "device-1",
	}
	for name, expected := range want {
		if got := headers[name]; got != expected {
			t.Fatalf("%s = %q, want %q", name, got, expected)
		}
	}
}
