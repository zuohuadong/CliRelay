package egress

import "time"

type Protocol string

const (
	ProtocolSOCKS5 Protocol = "socks5"
	ProtocolHTTP   Protocol = "http"
)

type Endpoint struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Protocol         Protocol  `json:"protocol"`
	Host             string    `json:"host"`
	Port             int       `json:"port"`
	Enabled          bool      `json:"enabled"`
	Username         string    `json:"username,omitempty"`
	Password         string    `json:"-"`
	ExpectedPublicIP string    `json:"expected_public_ip,omitempty"`
	PublicIP         string    `json:"public_ip,omitempty"`
	LatencyMS        int64     `json:"latency_ms,omitempty"`
	LastCheckedAt    time.Time `json:"last_checked_at,omitempty"`
	CheckStatus      string    `json:"status,omitempty"`
	CheckError       string    `json:"error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const (
	EndpointStatusHealthy           = "healthy"
	EndpointStatusUnhealthy         = "unhealthy"
	EndpointStatusIPMismatch        = "ip_mismatch"
	EndpointStatusDuplicatePublicIP = "duplicate_public_ip"
)

type Binding struct {
	Identity   string    `json:"identity"`
	EndpointID string    `json:"endpoint_id"`
	AuthFileID string    `json:"auth_file_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type BindingAssignment struct {
	Identity   string `json:"identity"`
	EndpointID string `json:"endpoint_id"`
	AuthFileID string `json:"auth_file_id,omitempty"`
}

type BindingConflict struct {
	Identity   string `json:"identity,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type BindingBatchPreview struct {
	Revision    string              `json:"revision"`
	Assignments []BindingAssignment `json:"assignments"`
	Conflicts   []BindingConflict   `json:"conflicts"`
	Valid       bool                `json:"valid"`
}

type BindingBatchResult struct {
	Revision string `json:"revision"`
	Applied  int    `json:"applied"`
}

type EndpointReadiness struct {
	EndpointID        string   `json:"endpoint_id"`
	Eligible          bool     `json:"eligible"`
	RuntimeReady      bool     `json:"runtime_ready"`
	HealthFresh       bool     `json:"health_fresh"`
	PublicIPMatches   bool     `json:"public_ip_matches"`
	DuplicatePublicIP bool     `json:"duplicate_public_ip"`
	Reasons           []string `json:"reasons"`
}

type EndpointAction string

const (
	EndpointActionDisable EndpointAction = "disable"
	EndpointActionDelete  EndpointAction = "delete"
)

type EndpointImpact struct {
	EndpointID           string         `json:"endpoint_id"`
	Action               EndpointAction `json:"action"`
	Revision             string         `json:"revision"`
	BindingCount         int            `json:"binding_count"`
	BindingIdentities    []string       `json:"binding_identities"`
	Allowed              bool           `json:"allowed"`
	RequiresConfirmation bool           `json:"requires_confirmation"`
	Blockers             []string       `json:"blockers"`
}

type TechnicalReadiness struct {
	Revision      string              `json:"revision"`
	Ready         bool                `json:"ready"`
	ReadyCount    int                 `json:"ready_count"`
	EndpointCount int                 `json:"endpoint_count"`
	Endpoints     []EndpointReadiness `json:"endpoints"`
	Blockers      []string            `json:"blockers"`
}

type ResolvedBinding struct {
	Binding  Binding  `json:"binding"`
	Endpoint Endpoint `json:"endpoint"`
}

type Counts struct {
	Endpoints        int `json:"endpoints"`
	EnabledEndpoints int `json:"enabled_endpoints"`
	Bindings         int `json:"bindings"`
}
