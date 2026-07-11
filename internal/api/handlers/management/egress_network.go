package management

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
)

type egressEndpointRequest struct {
	Name             string                     `json:"name"`
	Protocol         egress.Protocol            `json:"protocol"`
	Host             string                     `json:"host"`
	Port             int                        `json:"port"`
	Enabled          bool                       `json:"enabled"`
	SharingMode      egress.EndpointSharingMode `json:"sharing_mode"`
	Username         string                     `json:"username"`
	Password         string                     `json:"password"`
	ExpectedPublicIP string                     `json:"expected_public_ip"`
}

type egressEndpointPatch struct {
	Name             *string                     `json:"name"`
	Protocol         *egress.Protocol            `json:"protocol"`
	Host             *string                     `json:"host"`
	Port             *int                        `json:"port"`
	Enabled          *bool                       `json:"enabled"`
	SharingMode      *egress.EndpointSharingMode `json:"sharing_mode"`
	Username         *string                     `json:"username"`
	Password         *string                     `json:"password"`
	ExpectedPublicIP *string                     `json:"expected_public_ip"`
}

type egressBindingBatchRequest struct {
	Revision    string                     `json:"revision"`
	Confirmed   bool                       `json:"confirmed"`
	Assignments []egress.BindingAssignment `json:"assignments"`
}

type egressAuthInventory struct {
	Total            int
	Bound            int
	Unbound          int
	MissingAccountID int
	BoundNotReady    int
}

func (h *Handler) egress() *egress.Service {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.egressService
}

func (h *Handler) egressRuntimeEnabled() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg != nil && h.cfg.EgressNetwork.Enabled
}

func (h *Handler) GetEgressOverview(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, fmt.Errorf("%w: egress service is unavailable", egress.ErrEgressRequired))
		return
	}
	counts, err := service.Counts(c.Request.Context())
	if err != nil {
		writeEgressError(c, err)
		return
	}
	technical, err := service.TechnicalReadiness(c.Request.Context())
	if err != nil {
		writeEgressError(c, err)
		return
	}
	inventory, err := h.egressAuthInventory(c.Request.Context(), service)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	runtimeEnabled := h.egressRuntimeEnabled()
	reasons := append([]string(nil), technical.Blockers...)
	if inventory.Unbound > 0 {
		reasons = append(reasons, "unbound_codex_auths")
	}
	if inventory.MissingAccountID > 0 {
		reasons = append(reasons, "missing_account_id")
	}
	if inventory.BoundNotReady > 0 {
		reasons = append(reasons, "bound_endpoint_not_ready")
	}
	blockers := make([]gin.H, 0, len(reasons))
	for _, reason := range reasons {
		blockers = append(blockers, egressReadinessIssue(reason))
	}
	warnings := make([]gin.H, 0, 1)
	if !runtimeEnabled {
		warnings = append(warnings, egressReadinessIssue("runtime_disabled"))
	}
	readyToEnable := len(blockers) == 0
	combinedCounts := gin.H{
		"endpoints": counts.Endpoints, "enabled_endpoints": counts.EnabledEndpoints, "bindings": counts.Bindings,
		"codex_auths": inventory.Total, "bound_codex_auths": inventory.Bound,
		"unbound_codex_auths": inventory.Unbound, "missing_account_id": inventory.MissingAccountID,
		"bound_endpoint_not_ready": inventory.BoundNotReady,
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  runtimeEnabled,
		"revision": technical.Revision,
		"scope":    "application_egress",
		"policy": gin.H{
			"binding_mode": "per_endpoint", "failure_mode": "fail_closed",
			"readiness_scope": "application_egress", "host_kill_switch_enforced": false,
		},
		"counts": combinedCounts,
		"readiness": gin.H{
			"scope": "application_egress", "ready": readyToEnable, "ready_to_enable": readyToEnable,
			"codex_oauth_allowed": runtimeEnabled && technical.ReadyCount > 0,
			"revision":            technical.Revision, "reasons": reasons, "blockers": blockers, "warnings": warnings,
			"ready_endpoints": technical.ReadyCount, "endpoint_count": technical.EndpointCount,
			"endpoints": technical.Endpoints,
		},
	})
}

func egressReadinessIssue(code string) gin.H {
	messages := map[string]string{
		"no_endpoints":               "No egress endpoints are configured.",
		"no_runtime_ready_endpoints": "No endpoint is runtime ready.",
		"unbound_codex_auths":        "One or more Codex accounts are unbound.",
		"missing_account_id":         "One or more Codex accounts have no stable account identity.",
		"bound_endpoint_not_ready":   "One or more bound endpoints are not runtime ready.",
		"runtime_disabled":           "Egress runtime is disabled; Codex OAuth traffic is blocked.",
	}
	message := messages[code]
	if message == "" {
		message = code
	}
	return gin.H{"code": code, "message": message}
}

func (h *Handler) GetEgressEndpoints(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	items, err := service.ListEndpoints(c.Request.Context())
	if err != nil {
		writeEgressError(c, err)
		return
	}
	views := make([]gin.H, 0, len(items))
	for _, item := range items {
		view, errView := egressEndpointView(c.Request.Context(), service, item)
		if errView != nil {
			writeEgressError(c, errView)
			return
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

func (h *Handler) PostEgressEndpoint(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	var request egressEndpointRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": err.Error()}})
		return
	}
	created, err := service.CreateEndpoint(c.Request.Context(), endpointFromRequest(request))
	if err != nil {
		writeEgressError(c, err)
		return
	}
	view, err := egressEndpointView(c.Request.Context(), service, created)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusCreated, view)
}

func (h *Handler) PatchEgressEndpoint(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	endpoint, err := service.GetEndpoint(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeEgressError(c, err)
		return
	}
	var patch egressEndpointPatch
	if err = c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": err.Error()}})
		return
	}
	if patch.Enabled != nil && !*patch.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{
			"code": "egress_action_required", "message": "use endpoint impact and actions to disable an endpoint",
		}})
		return
	}
	applyEndpointPatch(&endpoint, patch)
	updated, err := service.UpdateEndpoint(c.Request.Context(), endpoint)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	view, err := egressEndpointView(c.Request.Context(), service, updated)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) DeleteEgressEndpoint(c *gin.Context) {
	c.JSON(http.StatusConflict, gin.H{"error": gin.H{
		"code": "egress_action_required", "message": "use endpoint impact and actions to delete an endpoint",
	}})
}

func (h *Handler) PostEgressEndpointCheck(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	checked, err := service.CheckEndpoint(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeEgressError(c, err)
		return
	}
	view, err := egressEndpointView(c.Request.Context(), service, checked)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) PostEgressEndpointImpact(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	var request struct {
		Action egress.EndpointAction `json:"action"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeInvalidEgressRequest(c, err)
		return
	}
	impact, err := service.EndpointImpact(c.Request.Context(), c.Param("id"), request.Action)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"endpoint_id": impact.EndpointID, "action": impact.Action, "revision": impact.Revision,
		"binding_count": impact.BindingCount, "binding_identities": impact.BindingIdentities,
		"allowed": impact.Allowed, "requires_confirmation": impact.RequiresConfirmation, "blockers": impact.Blockers,
	})
}

func (h *Handler) PostEgressEndpointAction(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	var request struct {
		Action    egress.EndpointAction `json:"action"`
		Revision  string                `json:"revision"`
		Confirmed bool                  `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeInvalidEgressRequest(c, err)
		return
	}
	if strings.TrimSpace(request.Revision) == "" {
		writeInvalidEgressRequest(c, errors.New("revision is required"))
		return
	}
	if err := service.ApplyEndpointAction(c.Request.Context(), c.Param("id"), request.Action, request.Confirmed, request.Revision); err != nil {
		writeEgressError(c, err)
		return
	}
	endpoint, err := service.GetEndpoint(c.Request.Context(), c.Param("id"))
	if err != nil {
		if request.Action == egress.EndpointActionDelete && errors.Is(err, egress.ErrEndpointNotFound) {
			c.Status(http.StatusNoContent)
			return
		}
		writeEgressError(c, err)
		return
	}
	view, err := egressEndpointView(c.Request.Context(), service, endpoint)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) GetEgressBindings(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	bindings, err := service.ListBindings(c.Request.Context())
	if err != nil {
		writeEgressError(c, err)
		return
	}
	byIdentity := make(map[string]egress.Binding, len(bindings))
	for _, binding := range bindings {
		byIdentity[binding.Identity] = binding
	}
	items := make([]gin.H, 0)
	endpointNames := make(map[string]string)
	if endpoints, errEndpoints := service.ListEndpoints(c.Request.Context()); errEndpoints == nil {
		for _, endpoint := range endpoints {
			endpointNames[endpoint.ID] = endpoint.Name
		}
	}
	seen := make(map[string]bool)
	manager := h.authManagerSnapshot()
	if manager != nil {
		for _, auth := range manager.List() {
			if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
				continue
			}
			accountID := ""
			if auth.Metadata != nil {
				accountID, _ = auth.Metadata["account_id"].(string)
			}
			identity, identityErr := egress.StableIdentity(accountID)
			item := gin.H{"auth_id": auth.ID, "account_label": auth.Label, "bound": false, "endpoint_id": ""}
			if identityErr != nil {
				item["identity"] = ""
				item["error"] = "missing account_id; refresh or re-login before migration"
				items = append(items, item)
				continue
			}
			item["identity"] = identity
			seen[identity] = true
			if binding, ok := byIdentity[identity]; ok {
				item["bound"] = true
				item["endpoint_id"] = binding.EndpointID
				item["endpoint_name"] = endpointNames[binding.EndpointID]
				item["updated_at"] = binding.UpdatedAt
				if item["auth_id"] == "" {
					item["auth_id"] = binding.AuthFileID
				}
			}
			items = append(items, item)
		}
	}
	for _, binding := range bindings {
		if seen[binding.Identity] {
			continue
		}
		items = append(items, gin.H{"identity": binding.Identity, "endpoint_id": binding.EndpointID, "endpoint_name": endpointNames[binding.EndpointID], "auth_id": binding.AuthFileID, "account_label": "", "bound": true, "updated_at": binding.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["auth_id"]) < fmt.Sprint(items[j]["auth_id"]) })
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) PostEgressBindingPreview(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	var request struct {
		Assignments []egress.BindingAssignment `json:"assignments"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		writeInvalidEgressRequest(c, err)
		return
	}
	if !validateStableCodexAssignments(c, request.Assignments) {
		return
	}
	preview, err := service.PreviewBindingBatch(c.Request.Context(), request.Assignments)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (h *Handler) PutEgressBindingBatch(c *gin.Context) {
	service := h.egress()
	if service == nil {
		writeEgressError(c, egress.ErrEgressRequired)
		return
	}
	var request egressBindingBatchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeInvalidEgressRequest(c, err)
		return
	}
	if !validateStableCodexAssignments(c, request.Assignments) {
		return
	}
	if !request.Confirmed {
		writeInvalidEgressRequest(c, errors.New("confirmed must be true"))
		return
	}
	if strings.TrimSpace(request.Revision) == "" {
		writeInvalidEgressRequest(c, errors.New("revision is required"))
		return
	}
	result, err := service.ApplyBindingBatch(c.Request.Context(), request.Revision, request.Assignments)
	if err != nil {
		writeEgressError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func endpointFromRequest(request egressEndpointRequest) egress.Endpoint {
	return egress.Endpoint{Name: request.Name, Protocol: request.Protocol, Host: request.Host, Port: request.Port, Enabled: request.Enabled, SharingMode: request.SharingMode, Username: request.Username, Password: request.Password, ExpectedPublicIP: request.ExpectedPublicIP}
}

func applyEndpointPatch(endpoint *egress.Endpoint, patch egressEndpointPatch) {
	if patch.Name != nil {
		endpoint.Name = *patch.Name
	}
	if patch.Protocol != nil {
		endpoint.Protocol = *patch.Protocol
	}
	if patch.Host != nil {
		endpoint.Host = *patch.Host
	}
	if patch.Port != nil {
		endpoint.Port = *patch.Port
	}
	if patch.Enabled != nil {
		endpoint.Enabled = *patch.Enabled
	}
	if patch.SharingMode != nil {
		endpoint.SharingMode = *patch.SharingMode
	}
	if patch.Username != nil {
		endpoint.Username = *patch.Username
	}
	if patch.Password != nil {
		endpoint.Password = *patch.Password
	}
	if patch.ExpectedPublicIP != nil {
		endpoint.ExpectedPublicIP = *patch.ExpectedPublicIP
	}
}

func egressEndpointView(ctx context.Context, service *egress.Service, endpoint egress.Endpoint) (gin.H, error) {
	readiness, err := service.EndpointReadiness(ctx, endpoint.ID)
	if err != nil {
		return nil, err
	}
	masked := (&url.URL{Scheme: string(endpoint.Protocol), Host: net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))}).String()
	return gin.H{
		"id": endpoint.ID, "name": endpoint.Name,
		"protocol": endpoint.Protocol, "host": endpoint.Host, "port": endpoint.Port,
		"enabled": endpoint.Enabled, "sharing_mode": endpoint.SharingMode,
		"username": endpoint.Username, "has_credentials": endpoint.Username != "" || endpoint.Password != "",
		"masked_url": masked, "expected_public_ip": endpoint.ExpectedPublicIP,
		"observed_public_ip": endpoint.PublicIP, "public_ip": endpoint.PublicIP, "latency_ms": endpoint.LatencyMS,
		"last_checked_at": endpoint.LastCheckedAt, "status": endpoint.CheckStatus, "error": endpoint.CheckError,
		"eligible": readiness.Eligible, "runtime_ready": readiness.RuntimeReady, "eligibility": readiness,
		"created_at": endpoint.CreatedAt, "updated_at": endpoint.UpdatedAt,
	}, nil
}

func writeEgressError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	code := egress.ErrorCode(err)
	switch {
	case errors.Is(err, egress.ErrEgressRequired), errors.Is(err, egress.ErrEndpointDisabled):
		status = http.StatusServiceUnavailable
		code = "egress_not_ready"
	case errors.Is(err, egress.ErrEndpointNotFound):
		status = http.StatusNotFound
	case errors.Is(err, egress.ErrEndpointInvalid), errors.Is(err, egress.ErrIdentityMismatch):
		status = http.StatusBadRequest
	case errors.Is(err, egress.ErrEndpointInUse), errors.Is(err, egress.ErrBindingConflict):
		status = http.StatusConflict
		code = "egress_endpoint_in_use"
	case errors.Is(err, egress.ErrRevisionConflict):
		status = http.StatusConflict
		code = "egress_plan_stale"
	case errors.Is(err, egress.ErrConfirmationRequired):
		status = http.StatusConflict
		code = "egress_confirmation_required"
	case errors.Is(err, egress.ErrCheckInProgress):
		status = http.StatusConflict
		code = "egress_check_in_progress"
	}
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": err.Error()}})
}

func writeInvalidEgressRequest(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "invalid_request", "message": err.Error()}})
}

func validateStableCodexAssignments(c *gin.Context, assignments []egress.BindingAssignment) bool {
	for _, assignment := range assignments {
		identity := strings.TrimSpace(assignment.Identity)
		if !egress.IsStableIdentity(identity) {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"code": "invalid_identity", "message": "identity must be codex:<sha256(account_id)>",
			}})
			return false
		}
	}
	return true
}

func (h *Handler) egressAuthInventory(ctx context.Context, service *egress.Service) (egressAuthInventory, error) {
	inventory := egressAuthInventory{}
	bindings, err := service.ListBindings(ctx)
	if err != nil {
		return inventory, err
	}
	byIdentity := make(map[string]egress.Binding, len(bindings))
	for _, binding := range bindings {
		byIdentity[binding.Identity] = binding
	}
	manager := h.authManagerSnapshot()
	if manager == nil {
		return inventory, nil
	}
	for _, auth := range manager.List() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		inventory.Total++
		accountID := ""
		if auth.Metadata != nil {
			accountID, _ = auth.Metadata["account_id"].(string)
		}
		identity, identityErr := egress.StableIdentity(accountID)
		if identityErr != nil {
			inventory.MissingAccountID++
			continue
		}
		binding, ok := byIdentity[identity]
		if !ok {
			inventory.Unbound++
			continue
		}
		inventory.Bound++
		readiness, readinessErr := service.EndpointReadiness(ctx, binding.EndpointID)
		if readinessErr != nil || !readiness.RuntimeReady {
			inventory.BoundNotReady++
		}
	}
	return inventory, nil
}
