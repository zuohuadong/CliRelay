package cliproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// 回归测试：Service.Run 必须启动出口健康检查循环。只覆盖 egress.Service.Start
// 不够，上游同步曾两次把 Run 里的启动调用丢掉，端点随后只能靠面板手动检查续命。
func TestServiceRunRefreshesEgressEndpointHealthWithoutManualCheck(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ip":"198.51.100.44"}`))
	}))
	t.Cleanup(proxyServer.Close)
	host, portText, errSplit := net.SplitHostPort(strings.TrimPrefix(proxyServer.URL, "http://"))
	if errSplit != nil {
		t.Fatalf("SplitHostPort() error = %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("Atoi() error = %v", errPort)
	}

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 0\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg := &config.Config{Port: 0, AuthDir: tempDir}
	cfg.EgressNetwork.Enabled = true
	cfg.EgressNetwork.EndpointCheckInterval = "1h"
	cfg.EgressNetwork.EndpointHealthTTL = "2h"
	cfg.EgressNetwork.ProbeURLs = []string{"https://probe.example/ip"}

	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if service.egressService == nil {
		t.Fatal("egress service was not initialized")
	}
	// 绕过启动时对探测地址的 https 过滤，让周期检查打到本地代理。
	cfg.EgressNetwork.ProbeURLs = []string{"http://probe.invalid/ip"}

	endpoint, errCreate := service.egressService.CreateEndpoint(context.Background(), egress.Endpoint{
		Name: "Loop check", Protocol: egress.ProtocolHTTP, Host: host, Port: port,
		Enabled: true, ExpectedPublicIP: "198.51.100.44",
	})
	if errCreate != nil {
		t.Fatalf("CreateEndpoint() error = %v", errCreate)
	}
	if !endpoint.LastCheckedAt.IsZero() {
		t.Fatal("new endpoint unexpectedly pre-checked")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = service.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for service shutdown")
		}
	})

	deadline := time.Now().Add(8 * time.Second)
	var checked egress.Endpoint
	for time.Now().Before(deadline) {
		current, errGet := service.egressService.GetEndpoint(context.Background(), endpoint.ID)
		if errGet != nil {
			t.Fatalf("GetEndpoint() error = %v", errGet)
		}
		checked = current
		if !checked.LastCheckedAt.IsZero() && checked.CheckStatus == egress.EndpointStatusHealthy && checked.PublicIP == endpoint.ExpectedPublicIP {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Run() did not refresh endpoint health automatically: status=%s ip=%s err=%s checked=%s", checked.CheckStatus, checked.PublicIP, checked.CheckError, checked.LastCheckedAt)
}
