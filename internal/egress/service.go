package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

type Resolver interface {
	Resolve(ctx context.Context, accountID string) (ResolvedEndpoint, error)
}

type ResolvedEndpoint struct {
	Endpoint Endpoint
	ProxyURL string
}

type Service struct {
	mu              sync.RWMutex
	cfg             *config.Config
	store           *Store
	now             func() time.Time
	probeURLs       []string
	lifecycleMu     sync.Mutex
	lifecycleCancel context.CancelFunc
	lifecycleWG     sync.WaitGroup
	wakeChecks      chan struct{}
	checksMu        sync.Mutex
	inFlightChecks  map[string]struct{}
	closeOnce       sync.Once
}

func NewService(cfg *config.Config, databasePath string) (*Service, error) {
	store, err := OpenStore(databasePath)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.SanitizeEgressNetwork()
	return &Service{
		cfg:            cfg,
		store:          store,
		now:            func() time.Time { return time.Now().UTC() },
		wakeChecks:     make(chan struct{}, 1),
		inFlightChecks: make(map[string]struct{}),
	}, nil
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		cancel := s.lifecycleCancel
		s.lifecycleCancel = nil
		s.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.lifecycleWG.Wait()
		if s.store != nil {
			closeErr = s.store.Close()
		}
	})
	return closeErr
}

// Start launches immediate and periodic endpoint health checks. It is
// idempotent; Close cancels and waits for the loop.
func (s *Service) Start(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("egress service is unavailable")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleCancel != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lifecycleCtx, cancel := context.WithCancel(ctx)
	s.lifecycleCancel = cancel
	s.lifecycleWG.Add(1)
	go s.endpointCheckLoop(lifecycleCtx)
	return nil
}

func (s *Service) Store() *Store {
	if s == nil {
		return nil
	}
	return s.store
}

func (s *Service) SetConfig(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	cfg.SanitizeEgressNetwork()
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	select {
	case s.wakeChecks <- struct{}{}:
	default:
	}
}

func (s *Service) endpointCheckLoop(ctx context.Context) {
	defer s.lifecycleWG.Done()
	s.checkEnabledEndpoints(ctx)
	checkTimer := time.NewTimer(s.maintenanceInterval())
	defer checkTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wakeChecks:
			s.checkEnabledEndpoints(ctx)
			resetTimer(checkTimer, s.maintenanceInterval())
		case <-checkTimer.C:
			s.checkEnabledEndpoints(ctx)
			checkTimer.Reset(s.maintenanceInterval())
		}
	}
}

func (s *Service) checkEnabledEndpoints(ctx context.Context) {
	endpoints, err := s.store.ListEndpoints(ctx)
	if err != nil {
		return
	}
	const maxConcurrentChecks = 4
	semaphore := make(chan struct{}, maxConcurrentChecks)
	var wg sync.WaitGroup
	for _, endpoint := range endpoints {
		if ctx.Err() != nil {
			break
		}
		if !endpoint.Enabled {
			continue
		}
		semaphore <- struct{}{}
		wg.Add(1)
		go func(endpointID string) {
			defer wg.Done()
			defer func() { <-semaphore }()
			_, _ = s.CheckEndpoint(ctx, endpointID)
		}(endpoint.ID)
	}
	wg.Wait()
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (s *Service) maintenanceInterval() time.Duration {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return 2 * time.Minute
	}
	interval, _ := time.ParseDuration(cfg.EgressNetwork.EndpointCheckInterval)
	if interval <= 0 {
		return 2 * time.Minute
	}
	return interval
}

func (s *Service) Resolve(ctx context.Context, accountID string) (ResolvedEndpoint, error) {
	if !s.enabled() {
		return ResolvedEndpoint{}, fmt.Errorf("%w: egress network is not enabled", ErrEgressRequired)
	}
	identity, err := StableIdentity(accountID)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	resolved, err := s.store.ResolveIdentity(ctx, identity)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	return s.resolveEndpoint(ctx, resolved.Endpoint)
}

func (s *Service) ResolveEndpoint(ctx context.Context, endpointID string) (ResolvedEndpoint, error) {
	if !s.enabled() {
		return ResolvedEndpoint{}, fmt.Errorf("%w: egress network is not enabled", ErrEgressRequired)
	}
	endpoint, err := s.store.GetEndpoint(ctx, endpointID)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	return s.resolveEndpoint(ctx, endpoint)
}

func (s *Service) resolveEndpoint(ctx context.Context, endpoint Endpoint) (ResolvedEndpoint, error) {
	readiness, err := s.endpointReadiness(ctx, endpoint)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	if !readiness.RuntimeReady {
		base := ErrEndpointDisabled
		if (endpoint.SharingMode != EndpointSharingModeShared && strings.TrimSpace(endpoint.ExpectedPublicIP) == "") || containsReason(readiness.Reasons, "expected_public_ip_invalid") {
			base = ErrEndpointInvalid
		}
		return ResolvedEndpoint{}, fmt.Errorf("%w: endpoint %s is not runtime ready: %s", base, endpoint.ID, strings.Join(readiness.Reasons, ","))
	}
	proxyURL, err := endpointProxyURL(endpoint)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	return ResolvedEndpoint{Endpoint: endpoint, ProxyURL: proxyURL}, nil
}

func (s *Service) EndpointReadiness(ctx context.Context, endpointID string) (EndpointReadiness, error) {
	endpoint, err := s.store.GetEndpoint(ctx, endpointID)
	if err != nil {
		return EndpointReadiness{}, err
	}
	return s.endpointReadiness(ctx, endpoint)
}

func (s *Service) endpointReadiness(ctx context.Context, endpoint Endpoint) (EndpointReadiness, error) {
	readiness := EndpointReadiness{EndpointID: endpoint.ID, Reasons: make([]string, 0)}
	if !endpoint.Enabled {
		readiness.Reasons = append(readiness.Reasons, "endpoint_disabled")
	}
	shared := endpoint.SharingMode == EndpointSharingModeShared
	expectedIP := strings.TrimSpace(endpoint.ExpectedPublicIP)
	if !shared && expectedIP == "" {
		readiness.Reasons = append(readiness.Reasons, "expected_public_ip_required")
	} else if !shared {
		parsed, err := parseEndpointIP(expectedIP)
		if err != nil {
			readiness.Reasons = append(readiness.Reasons, "expected_public_ip_invalid")
		} else {
			expectedIP = parsed.String()
		}
	}
	readiness.Eligible = endpoint.Enabled && (shared || expectedIP != "")
	readiness.HealthFresh = endpoint.CheckStatus == EndpointStatusHealthy && s.endpointHealthFresh(endpoint)
	if !readiness.HealthFresh {
		readiness.Reasons = append(readiness.Reasons, "endpoint_health_stale_or_unhealthy")
	}
	readiness.PublicIPMatches = shared || (expectedIP != "" && canonicalIP(endpoint.PublicIP) == expectedIP)
	if !readiness.PublicIPMatches {
		readiness.Reasons = append(readiness.Reasons, "public_ip_mismatch")
	}
	if !shared && strings.TrimSpace(endpoint.PublicIP) != "" {
		count, err := s.store.CountEndpointsByPublicIP(ctx, canonicalIP(endpoint.PublicIP), endpoint.ID)
		if err != nil {
			return EndpointReadiness{}, err
		}
		readiness.DuplicatePublicIP = count > 0
		if readiness.DuplicatePublicIP {
			readiness.Reasons = append(readiness.Reasons, "duplicate_public_ip")
		}
	}
	readiness.RuntimeReady = readiness.Eligible && readiness.HealthFresh && readiness.PublicIPMatches && !readiness.DuplicatePublicIP
	return readiness, nil
}

func containsReason(reasons []string, target string) bool {
	for _, reason := range reasons {
		if reason == target {
			return true
		}
	}
	return false
}

func (s *Service) endpointHealthFresh(endpoint Endpoint) bool {
	if endpoint.LastCheckedAt.IsZero() {
		return false
	}
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	text := ""
	if cfg != nil {
		text = cfg.EgressNetwork.EndpointHealthTTL
	}
	ttl, _ := time.ParseDuration(text)
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	age := s.now().Sub(endpoint.LastCheckedAt)
	return age >= 0 && age <= ttl
}

func (s *Service) CreateEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error) {
	return s.store.CreateEndpoint(ctx, endpoint)
}

func (s *Service) UpdateEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error) {
	return s.store.UpdateEndpoint(ctx, endpoint)
}

func (s *Service) CheckEndpoint(ctx context.Context, endpointID string) (Endpoint, error) {
	endpointID = strings.TrimSpace(endpointID)
	if !s.beginEndpointCheck(endpointID) {
		current, _ := s.store.GetEndpoint(ctx, endpointID)
		return current, fmt.Errorf("%w: endpoint %s", ErrCheckInProgress, endpointID)
	}
	defer s.endEndpointCheck(endpointID)
	resolved, transport, err := s.managementEndpointTransport(ctx, endpointID)
	if err != nil {
		return Endpoint{}, err
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	startedAt := s.now()
	shared := resolved.Endpoint.SharingMode == EndpointSharingModeShared
	expectedIP := canonicalIP(resolved.Endpoint.ExpectedPublicIP)
	validIPs := make([]string, 0)
	failures := make([]string, 0)
	publicIP := ""
	for _, probeURL := range s.endpointProbeURLs() {
		if ctx.Err() != nil {
			return Endpoint{}, ctx.Err()
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if reqErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", probeURL, reqErr))
			continue
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", probeURL, doErr))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if readErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", probeURL, readErr))
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			failures = append(failures, fmt.Sprintf("%s: status %d", probeURL, resp.StatusCode))
			continue
		}
		candidate := parseProbePublicIP(body)
		parsed, parseErr := parseEndpointIP(candidate)
		if parseErr != nil {
			failures = append(failures, fmt.Sprintf("%s: invalid public IP", probeURL))
			continue
		}
		candidate = parsed.String()
		validIPs = append(validIPs, candidate)
		if shared || candidate == expectedIP {
			publicIP = candidate
			break
		}
	}
	latency := s.now().Sub(startedAt).Milliseconds()
	checkedAt := s.now()
	status, checkError := EndpointStatusHealthy, ""
	if publicIP == "" && len(validIPs) == 0 {
		status = EndpointStatusUnhealthy
		checkError = "all public IP probes failed"
		if len(failures) > 0 {
			checkError += ": " + strings.Join(failures, "; ")
		}
		updated, persistErr := s.store.UpdateEndpointCheckIfRouteUnchanged(context.WithoutCancel(ctx), resolved.Endpoint, "", status, checkError, latency, checkedAt)
		if persistErr != nil {
			return updated, persistErr
		}
		return updated, errors.New(checkError)
	}
	if publicIP == "" {
		publicIP = validIPs[0]
		status = EndpointStatusIPMismatch
		checkError = fmt.Sprintf("observed public IP(s) %s do not match expected %s", strings.Join(validIPs, ","), resolved.Endpoint.ExpectedPublicIP)
	} else if !shared {
		duplicates, countErr := s.store.CountEndpointsByPublicIP(ctx, publicIP, endpointID)
		if countErr != nil {
			return Endpoint{}, countErr
		}
		if duplicates > 0 {
			status = EndpointStatusDuplicatePublicIP
			checkError = "observed public IP is already used by another endpoint"
		}
	}
	updated, updateErr := s.store.UpdateEndpointCheckIfRouteUnchanged(ctx, resolved.Endpoint, publicIP, status, checkError, latency, checkedAt)
	if updateErr != nil {
		return Endpoint{}, updateErr
	}
	if status != EndpointStatusHealthy {
		return updated, fmt.Errorf("%w: %s", ErrEndpointDisabled, checkError)
	}
	return updated, nil
}

func parseProbePublicIP(body []byte) string {
	var payload struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.IP) != "" {
		return strings.TrimSpace(payload.IP)
	}
	return strings.TrimSpace(string(body))
}

func (s *Service) endpointProbeURLs() []string {
	s.mu.RLock()
	overrides := append([]string(nil), s.probeURLs...)
	var configured []string
	if s.cfg != nil {
		configured = append(configured, s.cfg.EgressNetwork.ProbeURLs...)
	}
	s.mu.RUnlock()
	if len(overrides) > 0 {
		return overrides
	}
	return configured
}

func (s *Service) beginEndpointCheck(endpointID string) bool {
	if endpointID == "" {
		return false
	}
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	if _, exists := s.inFlightChecks[endpointID]; exists {
		return false
	}
	s.inFlightChecks[endpointID] = struct{}{}
	return true
}

func (s *Service) endEndpointCheck(endpointID string) {
	s.checksMu.Lock()
	delete(s.inFlightChecks, endpointID)
	s.checksMu.Unlock()
}

func (s *Service) HTTPClient(ctx context.Context, endpointID string, timeout time.Duration) (*http.Client, error) {
	resolved, err := s.ResolveEndpoint(ctx, endpointID)
	if err != nil {
		return nil, err
	}
	transport, mode, err := proxyutil.BuildHTTPTransport(resolved.ProxyURL)
	if err != nil || mode != proxyutil.ModeProxy || transport == nil {
		if err == nil {
			err = ErrEndpointInvalid
		}
		return nil, fmt.Errorf("%w: build strict endpoint transport: %v", ErrEndpointInvalid, err)
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client, nil
}

func (s *Service) DeleteEndpoint(ctx context.Context, id string) error {
	return s.store.DeleteEndpoint(ctx, id)
}

func (s *Service) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	return s.store.ListEndpoints(ctx)
}

func (s *Service) GetEndpoint(ctx context.Context, id string) (Endpoint, error) {
	return s.store.GetEndpoint(ctx, id)
}

func (s *Service) ListBindings(ctx context.Context) ([]Binding, error) {
	return s.store.ListBindings(ctx)
}

func (s *Service) PutBinding(ctx context.Context, binding Binding) error {
	assignment := BindingAssignment{Identity: binding.Identity, EndpointID: binding.EndpointID, AuthFileID: binding.AuthFileID}
	preview, err := s.PreviewBindingBatch(ctx, []BindingAssignment{assignment})
	if err != nil {
		return err
	}
	if !preview.Valid {
		return bindingConflictError(preview.Conflicts[0])
	}
	_, err = s.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments)
	return err
}

func (s *Service) PreviewBindingBatch(ctx context.Context, assignments []BindingAssignment) (BindingBatchPreview, error) {
	preview, err := s.store.PreviewBindingBatch(ctx, assignments)
	if err != nil {
		return BindingBatchPreview{}, err
	}
	seenEndpoints := make(map[string]struct{}, len(preview.Assignments))
	for _, assignment := range preview.Assignments {
		if strings.TrimSpace(assignment.EndpointID) == "" {
			continue
		}
		if _, seen := seenEndpoints[assignment.EndpointID]; seen {
			continue
		}
		seenEndpoints[assignment.EndpointID] = struct{}{}
		readiness, readinessErr := s.EndpointReadiness(ctx, assignment.EndpointID)
		if readinessErr != nil {
			if errors.Is(readinessErr, ErrEndpointNotFound) {
				continue
			}
			return BindingBatchPreview{}, readinessErr
		}
		if !readiness.RuntimeReady {
			preview.Conflicts = append(preview.Conflicts, BindingConflict{
				EndpointID: assignment.EndpointID,
				Code:       "endpoint_not_ready",
				Message:    "endpoint is not runtime ready: " + strings.Join(readiness.Reasons, ","),
			})
		}
	}
	preview.Valid = len(preview.Conflicts) == 0
	return preview, nil
}

func (s *Service) ApplyBindingBatch(ctx context.Context, expectedRevision string, assignments []BindingAssignment) (BindingBatchResult, error) {
	preview, err := s.PreviewBindingBatch(ctx, assignments)
	if err != nil {
		return BindingBatchResult{}, err
	}
	if expectedRevision = strings.TrimSpace(expectedRevision); expectedRevision != "" && expectedRevision != preview.Revision {
		return BindingBatchResult{}, fmt.Errorf("%w: expected %s, current %s", ErrRevisionConflict, expectedRevision, preview.Revision)
	}
	if !preview.Valid {
		return BindingBatchResult{}, bindingConflictError(preview.Conflicts[0])
	}
	return s.store.ApplyBindingBatch(ctx, preview.Revision, preview.Assignments)
}

func bindingConflictError(conflict BindingConflict) error {
	base := ErrBindingConflict
	switch conflict.Code {
	case "endpoint_not_found":
		base = ErrEndpointNotFound
	case "endpoint_disabled", "endpoint_not_ready":
		base = ErrEndpointDisabled
	case "expected_public_ip_required", "invalid_assignment":
		base = ErrEndpointInvalid
	}
	return fmt.Errorf("%w: %s", base, conflict.Message)
}

func (s *Service) EndpointImpact(ctx context.Context, endpointID string, action EndpointAction) (EndpointImpact, error) {
	return s.store.EndpointImpact(ctx, endpointID, action)
}

func (s *Service) ApplyEndpointAction(ctx context.Context, endpointID string, action EndpointAction, confirmed bool, expectedRevision string) error {
	return s.store.ApplyEndpointAction(ctx, endpointID, action, confirmed, expectedRevision)
}

func (s *Service) TechnicalReadiness(ctx context.Context) (TechnicalReadiness, error) {
	revision, err := s.store.BindingRevision(ctx)
	if err != nil {
		return TechnicalReadiness{}, err
	}
	endpoints, err := s.store.ListEndpoints(ctx)
	if err != nil {
		return TechnicalReadiness{}, err
	}
	snapshot := TechnicalReadiness{
		Revision: revision, EndpointCount: len(endpoints), Endpoints: make([]EndpointReadiness, 0, len(endpoints)), Blockers: make([]string, 0),
	}
	for _, endpoint := range endpoints {
		readiness, readinessErr := s.endpointReadiness(ctx, endpoint)
		if readinessErr != nil {
			return TechnicalReadiness{}, readinessErr
		}
		snapshot.Endpoints = append(snapshot.Endpoints, readiness)
		if readiness.RuntimeReady {
			snapshot.ReadyCount++
		}
	}
	if snapshot.EndpointCount == 0 {
		snapshot.Blockers = append(snapshot.Blockers, "no_endpoints")
	} else if snapshot.ReadyCount == 0 {
		snapshot.Blockers = append(snapshot.Blockers, "no_runtime_ready_endpoints")
	}
	snapshot.Ready = len(snapshot.Blockers) == 0
	return snapshot, nil
}

// managementEndpointTransport builds a strict proxy transport for operator
// health checks without requiring a previously healthy endpoint.
func (s *Service) managementEndpointTransport(ctx context.Context, endpointID string) (ResolvedEndpoint, *http.Transport, error) {
	endpoint, err := s.store.GetEndpoint(ctx, endpointID)
	if err != nil {
		return ResolvedEndpoint{}, nil, err
	}
	if !endpoint.Enabled {
		return ResolvedEndpoint{}, nil, fmt.Errorf("%w: endpoint %s is disabled", ErrEndpointDisabled, endpoint.ID)
	}
	if endpoint.SharingMode != EndpointSharingModeShared && strings.TrimSpace(endpoint.ExpectedPublicIP) == "" {
		return ResolvedEndpoint{}, nil, fmt.Errorf("%w: endpoint requires expected_public_ip", ErrEndpointInvalid)
	}
	proxyURL, err := endpointProxyURL(endpoint)
	if err != nil {
		return ResolvedEndpoint{}, nil, err
	}
	resolved := ResolvedEndpoint{Endpoint: endpoint, ProxyURL: proxyURL}
	transport, mode, err := proxyutil.BuildHTTPTransport(resolved.ProxyURL)
	if err != nil || mode != proxyutil.ModeProxy || transport == nil {
		if err == nil {
			err = ErrEndpointInvalid
		}
		return ResolvedEndpoint{}, nil, fmt.Errorf("%w: build management endpoint transport: %v", ErrEndpointInvalid, err)
	}
	return resolved, transport, nil
}

func (s *Service) Counts(ctx context.Context) (Counts, error) {
	return s.store.Counts(ctx)
}

func (s *Service) enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg != nil && s.cfg.EgressNetwork.Enabled
}

func endpointProxyURL(endpoint Endpoint) (string, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	u := &url.URL{
		Scheme: string(endpoint.Protocol),
		Host:   net.JoinHostPort(strings.TrimSpace(endpoint.Host), strconv.Itoa(endpoint.Port)),
	}
	if endpoint.Username != "" || endpoint.Password != "" {
		u.User = url.UserPassword(endpoint.Username, endpoint.Password)
	}
	return u.String(), nil
}

func parseEndpointIP(value string) (netip.Addr, error) {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, err
	}
	return address.Unmap(), nil
}
