package egress

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const schemaVersion = 2

func DatabasePathForConfig(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), "data", "egress.db")
}

type Store struct {
	db       *sql.DB
	lockFile *os.File
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("egress store path is required")
	}
	dataDir := filepath.Dir(path)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create egress data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("harden egress data directory: %w", err)
	}
	lockPath := path + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open egress writer lock: %w", err)
	}
	if err = os.Chmod(lockPath, 0o600); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("harden egress writer lock: %w", err)
	}
	if err = tryFileLock(lockFile); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("%w: %s", err, path)
	}
	dbFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = unlockFile(lockFile)
		_ = lockFile.Close()
		return nil, fmt.Errorf("create egress store: %w", err)
	}
	_ = dbFile.Close()
	if err = os.Chmod(path, 0o600); err != nil {
		_ = unlockFile(lockFile)
		_ = lockFile.Close()
		return nil, fmt.Errorf("harden egress store: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = unlockFile(lockFile)
		_ = lockFile.Close()
		return nil, fmt.Errorf("open egress store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, lockFile: lockFile}
	if err = store.migrate(context.Background()); err != nil {
		_ = db.Close()
		_ = unlockFile(lockFile)
		_ = lockFile.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.db != nil {
		first = s.db.Close()
		s.db = nil
	}
	if s.lockFile != nil {
		if err := unlockFile(s.lockFile); err != nil && first == nil {
			first = err
		}
		if err := s.lockFile.Close(); err != nil && first == nil {
			first = err
		}
		s.lockFile = nil
	}
	return first
}

func (s *Store) migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("egress store is nil")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin egress migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var legacyTables int
	if err = tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sqlite_master
WHERE type = 'table' AND name IN ('egress_nodes', 'egress_state')
`).Scan(&legacyTables); err != nil {
		return fmt.Errorf("inspect egress schema: %w", err)
	}
	if legacyTables > 0 {
		return fmt.Errorf("legacy Headscale egress schema is unsupported; stop CliRelay, back up and remove egress.db files, then rebuild endpoints and bindings")
	}

	if _, err = tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS egress_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS egress_endpoints (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  protocol TEXT NOT NULL,
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  username TEXT NOT NULL DEFAULT '',
  password TEXT NOT NULL DEFAULT '',
  expected_public_ip TEXT NOT NULL DEFAULT '',
  public_ip TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  last_checked_at TEXT NOT NULL DEFAULT '',
  check_status TEXT NOT NULL DEFAULT '',
  check_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_egress_endpoints_expected_public_ip_unique ON egress_endpoints(expected_public_ip) WHERE expected_public_ip <> '';
CREATE TABLE IF NOT EXISTS egress_bindings (
  identity TEXT PRIMARY KEY,
  endpoint_id TEXT NOT NULL,
  auth_file_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(endpoint_id) REFERENCES egress_endpoints(id) ON UPDATE CASCADE ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_egress_bindings_endpoint ON egress_bindings(endpoint_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_egress_bindings_endpoint_unique ON egress_bindings(endpoint_id);
`); err != nil {
		return fmt.Errorf("create egress schema: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO egress_schema_migrations(version, applied_at) VALUES (?, ?)`, schemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record egress schema: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit egress migration: %w", err)
	}
	return nil
}

func StableIdentity(accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", fmt.Errorf("%w: codex account_id is required", ErrEgressRequired)
	}
	digest := sha256.Sum256([]byte(accountID))
	return "codex:" + hex.EncodeToString(digest[:]), nil
}

func IsStableIdentity(identity string) bool {
	identity = strings.TrimSpace(identity)
	if len(identity) != len("codex:")+sha256.Size*2 || !strings.HasPrefix(identity, "codex:") {
		return false
	}
	digest := strings.TrimPrefix(identity, "codex:")
	if digest != strings.ToLower(digest) {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func (s *Store) CreateEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error) {
	endpoint.ID = strings.TrimSpace(endpoint.ID)
	if endpoint.ID == "" {
		endpoint.ID = uuid.NewString()
	}
	if err := validateEndpoint(endpoint); err != nil {
		return Endpoint{}, err
	}
	endpoint.ExpectedPublicIP = canonicalIP(endpoint.ExpectedPublicIP)
	now := time.Now().UTC()
	endpoint.CreatedAt = now
	endpoint.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
INSERT INTO egress_endpoints(id, name, protocol, host, port, enabled, username, password, expected_public_ip, public_ip, latency_ms, last_checked_at, check_status, check_error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, endpoint.ID, strings.TrimSpace(endpoint.Name), endpoint.Protocol, strings.TrimSpace(endpoint.Host), endpoint.Port, boolInt(endpoint.Enabled), endpoint.Username, endpoint.Password, strings.TrimSpace(endpoint.ExpectedPublicIP), endpoint.PublicIP, endpoint.LatencyMS, formatTime(endpoint.LastCheckedAt), endpoint.CheckStatus, endpoint.CheckError, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if isExpectedIPConstraint(err) {
			return Endpoint{}, fmt.Errorf("%w: expected_public_ip is already assigned", ErrBindingConflict)
		}
		return Endpoint{}, fmt.Errorf("create egress endpoint: %w", err)
	}
	return endpoint, nil
}

func (s *Store) UpdateEndpoint(ctx context.Context, endpoint Endpoint) (Endpoint, error) {
	endpoint.ID = strings.TrimSpace(endpoint.ID)
	if endpoint.ID == "" {
		return Endpoint{}, ErrEndpointNotFound
	}
	current, err := s.GetEndpoint(ctx, endpoint.ID)
	if err != nil {
		return Endpoint{}, err
	}
	if err := validateEndpoint(endpoint); err != nil {
		return Endpoint{}, err
	}
	endpoint.ExpectedPublicIP = canonicalIP(endpoint.ExpectedPublicIP)
	publicIP := current.PublicIP
	latencyMS := current.LatencyMS
	lastCheckedAt := current.LastCheckedAt
	checkStatus := current.CheckStatus
	checkError := current.CheckError
	if endpointRouteChanged(current, endpoint) || (!current.Enabled && endpoint.Enabled) {
		publicIP = ""
		latencyMS = 0
		lastCheckedAt = time.Time{}
		checkStatus = ""
		checkError = ""
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
UPDATE egress_endpoints SET name=?, protocol=?, host=?, port=?, enabled=?, username=?, password=?, expected_public_ip=?, public_ip=?, latency_ms=?, last_checked_at=?, check_status=?, check_error=?, updated_at=? WHERE id=?`, strings.TrimSpace(endpoint.Name), endpoint.Protocol, strings.TrimSpace(endpoint.Host), endpoint.Port, boolInt(endpoint.Enabled), endpoint.Username, endpoint.Password, strings.TrimSpace(endpoint.ExpectedPublicIP), publicIP, latencyMS, formatTime(lastCheckedAt), checkStatus, checkError, now.Format(time.RFC3339Nano), endpoint.ID)
	if err != nil {
		if isExpectedIPConstraint(err) {
			return Endpoint{}, fmt.Errorf("%w: expected_public_ip is already assigned", ErrBindingConflict)
		}
		return Endpoint{}, fmt.Errorf("update egress endpoint: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Endpoint{}, ErrEndpointNotFound
	}
	return s.GetEndpoint(ctx, endpoint.ID)
}

func endpointRouteChanged(current, next Endpoint) bool {
	return current.Protocol != next.Protocol ||
		strings.TrimSpace(current.Host) != strings.TrimSpace(next.Host) ||
		current.Port != next.Port ||
		current.Username != next.Username ||
		current.Password != next.Password ||
		canonicalIP(current.ExpectedPublicIP) != canonicalIP(next.ExpectedPublicIP)
}

func (s *Store) GetEndpoint(ctx context.Context, id string) (Endpoint, error) {
	var endpoint Endpoint
	var enabled int
	var createdAt, updatedAt string
	var lastCheckedAt string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, protocol, host, port, enabled, username, password, expected_public_ip, public_ip, latency_ms, last_checked_at, check_status, check_error, created_at, updated_at FROM egress_endpoints WHERE id=?`, strings.TrimSpace(id)).Scan(&endpoint.ID, &endpoint.Name, &endpoint.Protocol, &endpoint.Host, &endpoint.Port, &enabled, &endpoint.Username, &endpoint.Password, &endpoint.ExpectedPublicIP, &endpoint.PublicIP, &endpoint.LatencyMS, &lastCheckedAt, &endpoint.CheckStatus, &endpoint.CheckError, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Endpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return Endpoint{}, err
	}
	endpoint.Enabled = enabled != 0
	endpoint.LastCheckedAt = parseTime(lastCheckedAt)
	endpoint.CreatedAt = parseTime(createdAt)
	endpoint.UpdatedAt = parseTime(updatedAt)
	return endpoint, nil
}

func (s *Store) UpdateEndpointCheck(ctx context.Context, id, publicIP, status, checkError string, latencyMS int64, checkedAt time.Time) (Endpoint, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE egress_endpoints SET public_ip=?, latency_ms=?, last_checked_at=?, check_status=?, check_error=?, updated_at=? WHERE id=?`, strings.TrimSpace(publicIP), latencyMS, formatTime(checkedAt), strings.TrimSpace(status), strings.TrimSpace(checkError), time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id))
	if err != nil {
		return Endpoint{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Endpoint{}, ErrEndpointNotFound
	}
	return s.GetEndpoint(ctx, id)
}

// UpdateEndpointCheckIfRouteUnchanged persists a probe only when the endpoint
// route and credential fields still match the snapshot used to perform it.
// This prevents an in-flight check of an old proxy route from marking a newly
// edited route healthy.
func (s *Store) UpdateEndpointCheckIfRouteUnchanged(ctx context.Context, expected Endpoint, publicIP, status, checkError string, latencyMS int64, checkedAt time.Time) (Endpoint, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE egress_endpoints
SET public_ip=?, latency_ms=?, last_checked_at=?, check_status=?, check_error=?, updated_at=?
WHERE id=?
  AND protocol=? AND host=? AND port=? AND enabled=?
  AND username=? AND password=? AND expected_public_ip=?`,
		strings.TrimSpace(publicIP), latencyMS, formatTime(checkedAt), strings.TrimSpace(status), strings.TrimSpace(checkError), time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(expected.ID), expected.Protocol, strings.TrimSpace(expected.Host), expected.Port, boolInt(expected.Enabled), expected.Username, expected.Password, canonicalIP(expected.ExpectedPublicIP),
	)
	if err != nil {
		return Endpoint{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		current, getErr := s.GetEndpoint(ctx, expected.ID)
		if getErr != nil {
			return Endpoint{}, getErr
		}
		return current, fmt.Errorf("%w: endpoint route changed during health check", ErrRevisionConflict)
	}
	return s.GetEndpoint(ctx, expected.ID)
}

func (s *Store) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM egress_endpoints ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(ids))
	for _, id := range ids {
		endpoint, errGet := s.GetEndpoint(ctx, id)
		if errGet != nil {
			return nil, errGet
		}
		out = append(out, endpoint)
	}
	return out, nil
}

func (s *Store) CountEndpointsByPublicIP(ctx context.Context, publicIP, excludeID string) (int, error) {
	publicIP = strings.TrimSpace(publicIP)
	if publicIP == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM egress_endpoints WHERE public_ip=? AND id<>?`, publicIP, strings.TrimSpace(excludeID)).Scan(&count)
	return count, err
}

func (s *Store) DeleteEndpoint(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM egress_endpoints WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key constraint") {
			return ErrEndpointInUse
		}
		return fmt.Errorf("delete egress endpoint: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrEndpointNotFound
	}
	return nil
}

func (s *Store) EndpointImpact(ctx context.Context, id string, action EndpointAction) (EndpointImpact, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EndpointImpact{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if action != EndpointActionDisable && action != EndpointActionDelete {
		return EndpointImpact{}, fmt.Errorf("%w: unsupported endpoint action", ErrEndpointInvalid)
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM egress_endpoints WHERE id=?`, strings.TrimSpace(id)).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return EndpointImpact{}, ErrEndpointNotFound
	} else if err != nil {
		return EndpointImpact{}, err
	}
	revision, err := bindingRevisionTx(ctx, tx)
	if err != nil {
		return EndpointImpact{}, err
	}
	identities, err := endpointBindingIdentitiesTx(ctx, tx, id)
	if err != nil {
		return EndpointImpact{}, err
	}
	impact := EndpointImpact{
		EndpointID: strings.TrimSpace(id), Action: action, Revision: revision,
		BindingCount: len(identities), BindingIdentities: identities,
		Allowed:              action == EndpointActionDisable || len(identities) == 0,
		RequiresConfirmation: true,
		Blockers:             make([]string, 0),
	}
	if action == EndpointActionDelete && len(identities) != 0 {
		impact.Blockers = append(impact.Blockers, "endpoint_has_bindings")
	}
	return impact, nil
}

func (s *Store) ApplyEndpointAction(ctx context.Context, id string, action EndpointAction, confirmed bool, expectedRevision string) error {
	if !confirmed {
		return fmt.Errorf("%w: endpoint action confirmation is required", ErrConfirmationRequired)
	}
	if action != EndpointActionDisable && action != EndpointActionDelete {
		return fmt.Errorf("%w: unsupported endpoint action", ErrEndpointInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	currentRevision, err := bindingRevisionTx(ctx, tx)
	if err != nil {
		return err
	}
	if expectedRevision = strings.TrimSpace(expectedRevision); expectedRevision != "" && expectedRevision != currentRevision {
		return fmt.Errorf("%w: expected %s, current %s", ErrRevisionConflict, expectedRevision, currentRevision)
	}
	identities, err := endpointBindingIdentitiesTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if action == EndpointActionDelete && len(identities) != 0 {
		return fmt.Errorf("%w: endpoint has %d binding(s)", ErrEndpointInUse, len(identities))
	}
	var result sql.Result
	switch action {
	case EndpointActionDisable:
		result, err = tx.ExecContext(ctx, `UPDATE egress_endpoints SET enabled=0, updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id))
	case EndpointActionDelete:
		result, err = tx.ExecContext(ctx, `DELETE FROM egress_endpoints WHERE id=?`, strings.TrimSpace(id))
	}
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrEndpointNotFound
	}
	return tx.Commit()
}

func endpointBindingIdentitiesTx(ctx context.Context, tx *sql.Tx, endpointID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT identity FROM egress_bindings WHERE endpoint_id=? ORDER BY identity`, strings.TrimSpace(endpointID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make([]string, 0)
	for rows.Next() {
		var identity string
		if err = rows.Scan(&identity); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (s *Store) PutBinding(ctx context.Context, binding Binding) error {
	_, err := s.ApplyBindingBatch(ctx, "", []BindingAssignment{{
		Identity: binding.Identity, EndpointID: binding.EndpointID, AuthFileID: binding.AuthFileID,
	}})
	return err
}

// PreviewBindingBatch validates the final exclusive assignment set without
// mutating it. Revision covers bindings and endpoint eligibility inputs.
func (s *Store) PreviewBindingBatch(ctx context.Context, assignments []BindingAssignment) (BindingBatchPreview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BindingBatchPreview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	revision, err := bindingRevisionTx(ctx, tx)
	if err != nil {
		return BindingBatchPreview{}, err
	}
	normalized := normalizeAssignments(assignments)
	if err = preserveExistingAuthFileIDsTx(ctx, tx, normalized); err != nil {
		return BindingBatchPreview{}, err
	}
	conflicts, err := validateBindingAssignmentsTx(ctx, tx, normalized)
	if err != nil {
		return BindingBatchPreview{}, err
	}
	return BindingBatchPreview{
		Revision: revision, Assignments: normalized, Conflicts: conflicts, Valid: len(conflicts) == 0,
	}, nil
}

// ApplyBindingBatch atomically validates and applies all assignments. When an
// expected revision is supplied, any intervening endpoint or binding mutation
// rejects the whole batch.
func (s *Store) ApplyBindingBatch(ctx context.Context, expectedRevision string, assignments []BindingAssignment) (BindingBatchResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BindingBatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	currentRevision, err := bindingRevisionTx(ctx, tx)
	if err != nil {
		return BindingBatchResult{}, err
	}
	if expectedRevision = strings.TrimSpace(expectedRevision); expectedRevision != "" && expectedRevision != currentRevision {
		return BindingBatchResult{}, fmt.Errorf("%w: expected %s, current %s", ErrRevisionConflict, expectedRevision, currentRevision)
	}
	normalized := normalizeAssignments(assignments)
	if err = preserveExistingAuthFileIDsTx(ctx, tx, normalized); err != nil {
		return BindingBatchResult{}, err
	}
	conflicts, err := validateBindingAssignmentsTx(ctx, tx, normalized)
	if err != nil {
		return BindingBatchResult{}, err
	}
	if len(conflicts) != 0 {
		base := ErrBindingConflict
		switch conflicts[0].Code {
		case "endpoint_not_found":
			base = ErrEndpointNotFound
		case "endpoint_disabled":
			base = ErrEndpointDisabled
		case "expected_public_ip_required", "invalid_assignment":
			base = ErrEndpointInvalid
		}
		return BindingBatchResult{}, fmt.Errorf("%w: %s", base, conflicts[0].Message)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, assignment := range normalized {
		if _, err = tx.ExecContext(ctx, `DELETE FROM egress_bindings WHERE identity=?`, assignment.Identity); err != nil {
			return BindingBatchResult{}, fmt.Errorf("prepare egress binding for %s: %w", assignment.Identity, err)
		}
	}
	for _, assignment := range normalized {
		if assignment.EndpointID == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO egress_bindings(identity, endpoint_id, auth_file_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
`, assignment.Identity, assignment.EndpointID, assignment.AuthFileID, now, now); err != nil {
			return BindingBatchResult{}, fmt.Errorf("apply egress binding for %s: %w", assignment.Identity, err)
		}
	}
	revision, err := bindingRevisionTx(ctx, tx)
	if err != nil {
		return BindingBatchResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return BindingBatchResult{}, err
	}
	return BindingBatchResult{Revision: revision, Applied: len(normalized)}, nil
}

func (s *Store) BindingRevision(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	return bindingRevisionTx(ctx, tx)
}

func normalizeAssignments(assignments []BindingAssignment) []BindingAssignment {
	out := append([]BindingAssignment(nil), assignments...)
	for i := range out {
		out[i].Identity = strings.TrimSpace(out[i].Identity)
		out[i].EndpointID = strings.TrimSpace(out[i].EndpointID)
		out[i].AuthFileID = strings.TrimSpace(out[i].AuthFileID)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identity == out[j].Identity {
			return out[i].EndpointID < out[j].EndpointID
		}
		return out[i].Identity < out[j].Identity
	})
	return out
}

func preserveExistingAuthFileIDsTx(ctx context.Context, tx *sql.Tx, assignments []BindingAssignment) error {
	needsExisting := false
	for i := range assignments {
		if assignments[i].EndpointID != "" && assignments[i].AuthFileID == "" {
			needsExisting = true
			break
		}
	}
	if !needsExisting {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT identity, auth_file_id FROM egress_bindings`)
	if err != nil {
		return fmt.Errorf("load existing egress binding auth files: %w", err)
	}
	existing := make(map[string]string)
	for rows.Next() {
		var identity, authFileID string
		if err = rows.Scan(&identity, &authFileID); err != nil {
			_ = rows.Close()
			return err
		}
		existing[identity] = authFileID
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for i := range assignments {
		if assignments[i].EndpointID == "" || assignments[i].AuthFileID != "" {
			continue
		}
		assignments[i].AuthFileID = strings.TrimSpace(existing[assignments[i].Identity])
	}
	return nil
}

func validateBindingAssignmentsTx(ctx context.Context, tx *sql.Tx, assignments []BindingAssignment) ([]BindingConflict, error) {
	conflicts := make([]BindingConflict, 0)
	seenIdentity := make(map[string]struct{}, len(assignments))
	final := make(map[string]string)
	rows, err := tx.QueryContext(ctx, `SELECT identity, endpoint_id FROM egress_bindings`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var identity, endpointID string
		if err = rows.Scan(&identity, &endpointID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		final[identity] = endpointID
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for _, assignment := range assignments {
		if !IsStableIdentity(assignment.Identity) {
			conflicts = append(conflicts, BindingConflict{Identity: assignment.Identity, EndpointID: assignment.EndpointID, Code: "invalid_assignment", Message: "identity must be codex:<sha256(account_id)>"})
			continue
		}
		if _, duplicate := seenIdentity[assignment.Identity]; duplicate {
			conflicts = append(conflicts, BindingConflict{Identity: assignment.Identity, EndpointID: assignment.EndpointID, Code: "duplicate_identity", Message: "identity appears more than once in the batch"})
			continue
		}
		seenIdentity[assignment.Identity] = struct{}{}
		if assignment.EndpointID == "" {
			delete(final, assignment.Identity)
			continue
		}
		var enabled int
		var expectedIP string
		err = tx.QueryRowContext(ctx, `SELECT enabled, expected_public_ip FROM egress_endpoints WHERE id=?`, assignment.EndpointID).Scan(&enabled, &expectedIP)
		if errors.Is(err, sql.ErrNoRows) {
			conflicts = append(conflicts, BindingConflict{Identity: assignment.Identity, EndpointID: assignment.EndpointID, Code: "endpoint_not_found", Message: "endpoint does not exist"})
			continue
		}
		if err != nil {
			return nil, err
		}
		if enabled == 0 {
			conflicts = append(conflicts, BindingConflict{Identity: assignment.Identity, EndpointID: assignment.EndpointID, Code: "endpoint_disabled", Message: "endpoint is disabled"})
			continue
		}
		if strings.TrimSpace(expectedIP) == "" {
			conflicts = append(conflicts, BindingConflict{Identity: assignment.Identity, EndpointID: assignment.EndpointID, Code: "expected_public_ip_required", Message: "enabled endpoint requires expected_public_ip"})
			continue
		}
		final[assignment.Identity] = assignment.EndpointID
	}
	owners := make(map[string]string, len(final))
	identities := make([]string, 0, len(final))
	for identity := range final {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		endpointID := final[identity]
		if owner, exists := owners[endpointID]; exists && owner != identity {
			conflicts = append(conflicts, BindingConflict{Identity: identity, EndpointID: endpointID, Code: "endpoint_already_bound", Message: fmt.Sprintf("endpoint is exclusively assigned to %s", owner)})
			continue
		}
		owners[endpointID] = identity
	}
	return conflicts, nil
}

func bindingRevisionTx(ctx context.Context, tx *sql.Tx) (string, error) {
	hash := sha256.New()
	rows, err := tx.QueryContext(ctx, `SELECT identity, endpoint_id, auth_file_id, updated_at FROM egress_bindings ORDER BY identity`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var values [4]string
		if err = rows.Scan(&values[0], &values[1], &values[2], &values[3]); err != nil {
			_ = rows.Close()
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "b\x00%s\x00%s\x00%s\x00%s\n", values[0], values[1], values[2], values[3])
	}
	if err = rows.Close(); err != nil {
		return "", err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, enabled, expected_public_ip, public_ip, check_status, last_checked_at, updated_at FROM egress_endpoints ORDER BY id`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, expectedIP, publicIP, checkStatus, lastCheckedAt, updatedAt string
		var enabled int
		if err = rows.Scan(&id, &enabled, &expectedIP, &publicIP, &checkStatus, &lastCheckedAt, &updatedAt); err != nil {
			_ = rows.Close()
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "e\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\n", id, enabled, expectedIP, publicIP, checkStatus, lastCheckedAt, updatedAt)
	}
	if err = rows.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) ResolveIdentity(ctx context.Context, identity string) (ResolvedBinding, error) {
	var out ResolvedBinding
	var endpointEnabled int
	var bindingCreated, bindingUpdated, endpointCreated, endpointUpdated, lastCheckedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT b.identity, b.endpoint_id, b.auth_file_id, b.created_at, b.updated_at,
       e.id, e.name, e.protocol, e.host, e.port, e.enabled, e.username, e.password, e.expected_public_ip, e.public_ip, e.latency_ms, e.last_checked_at, e.check_status, e.check_error, e.created_at, e.updated_at
FROM egress_bindings b
JOIN egress_endpoints e ON e.id=b.endpoint_id
WHERE b.identity=?`, strings.TrimSpace(identity)).Scan(
		&out.Binding.Identity, &out.Binding.EndpointID, &out.Binding.AuthFileID, &bindingCreated, &bindingUpdated,
		&out.Endpoint.ID, &out.Endpoint.Name, &out.Endpoint.Protocol, &out.Endpoint.Host, &out.Endpoint.Port, &endpointEnabled, &out.Endpoint.Username, &out.Endpoint.Password, &out.Endpoint.ExpectedPublicIP, &out.Endpoint.PublicIP, &out.Endpoint.LatencyMS, &lastCheckedAt, &out.Endpoint.CheckStatus, &out.Endpoint.CheckError, &endpointCreated, &endpointUpdated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ResolvedBinding{}, ErrEgressRequired
	}
	if err != nil {
		return ResolvedBinding{}, err
	}
	out.Endpoint.Enabled = endpointEnabled != 0
	out.Endpoint.LastCheckedAt = parseTime(lastCheckedAt)
	out.Binding.CreatedAt = parseTime(bindingCreated)
	out.Binding.UpdatedAt = parseTime(bindingUpdated)
	out.Endpoint.CreatedAt = parseTime(endpointCreated)
	out.Endpoint.UpdatedAt = parseTime(endpointUpdated)
	return out, nil
}

func (s *Store) ListBindings(ctx context.Context) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT identity, endpoint_id, auth_file_id, created_at, updated_at FROM egress_bindings ORDER BY identity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Binding, 0)
	for rows.Next() {
		var binding Binding
		var createdAt, updatedAt string
		if err = rows.Scan(&binding.Identity, &binding.EndpointID, &binding.AuthFileID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		binding.CreatedAt = parseTime(createdAt)
		binding.UpdatedAt = parseTime(updatedAt)
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var counts Counts
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM egress_endpoints`:                 &counts.Endpoints,
		`SELECT COUNT(*) FROM egress_endpoints WHERE enabled=1`: &counts.EnabledEndpoints,
		`SELECT COUNT(*) FROM egress_bindings`:                  &counts.Bindings,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return Counts{}, err
		}
	}
	return counts, nil
}

func validateEndpoint(endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.Name) == "" || strings.TrimSpace(endpoint.Host) == "" || endpoint.Port < 1 || endpoint.Port > 65535 {
		return ErrEndpointInvalid
	}
	switch endpoint.Protocol {
	case ProtocolSOCKS5, ProtocolHTTP:
	default:
		return ErrEndpointInvalid
	}
	expectedIP := strings.TrimSpace(endpoint.ExpectedPublicIP)
	if endpoint.Enabled && expectedIP == "" {
		return fmt.Errorf("%w: enabled endpoint requires expected_public_ip", ErrEndpointInvalid)
	}
	if expectedIP != "" {
		if _, err := parseEndpointIP(expectedIP); err != nil {
			return fmt.Errorf("%w: expected_public_ip must be an IP address", ErrEndpointInvalid)
		}
	}
	return nil
}

func canonicalIP(value string) string {
	parsed, err := parseEndpointIP(value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return parsed.String()
}

func isExpectedIPConstraint(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") && strings.Contains(text, "expected_public_ip")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
