package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// MaxCredentialWeight bounds operator-provided routing weights.
const MaxCredentialWeight int64 = 1_000_000

// ParseCredentialWeight accepts the scalar shapes produced by JSON/YAML decoding.
func ParseCredentialWeight(value any) (int64, error) {
	var weight int64
	switch typed := value.(type) {
	case int:
		weight = int64(typed)
	case int64:
		weight = typed
	case float64:
		if math.Trunc(typed) != typed || typed > math.MaxInt64 || typed < math.MinInt64 {
			return 0, fmt.Errorf("weight must be an integer")
		}
		weight = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("weight must be an integer")
		}
		weight = parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("weight must be an integer")
		}
		weight = parsed
	default:
		return 0, fmt.Errorf("weight must be an integer")
	}
	if weight > MaxCredentialWeight {
		return 0, fmt.Errorf("weight must not exceed %d", MaxCredentialWeight)
	}
	if weight <= 0 {
		return 0, nil
	}
	return weight, nil
}

// ApplyAuthWeightMetadata promotes an auth-file weight into immutable attributes.
func ApplyAuthWeightMetadata(auth *Auth) error {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	raw, ok := auth.Metadata[AttributeWeight]
	if !ok {
		return nil
	}
	weight, err := ParseCredentialWeight(raw)
	if err != nil {
		return err
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributeWeight] = strconv.FormatInt(weight, 10)
	return nil
}

func authWeight(auth *Auth) int64 {
	if auth == nil {
		return 0
	}
	if auth.Attributes != nil {
		if raw, ok := auth.Attributes[AttributeWeight]; ok {
			weight, err := ParseCredentialWeight(raw)
			if err != nil {
				return 0
			}
			return weight
		}
	}
	if auth.Metadata != nil {
		if raw, ok := auth.Metadata[AttributeWeight]; ok {
			weight, err := ParseCredentialWeight(raw)
			if err != nil {
				return 0
			}
			return weight
		}
	}
	return 1
}

type weightedRoundRobinState struct {
	current map[string]int64
	weights map[string]int64
}

// WeightedRoundRobinSelector applies smooth weighted round-robin per provider/model.
// Missing weights default to one; non-positive weights exclude a credential.
type WeightedRoundRobinSelector struct {
	mu      sync.Mutex
	states  map[string]*weightedRoundRobinState
	maxKeys int
}

func (s *WeightedRoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, err := getAvailableAuths(auths, provider, model, time.Now())
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	key := strings.ToLower(strings.TrimSpace(provider)) + ":" + canonicalModelKey(model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*weightedRoundRobinState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*weightedRoundRobinState)
	}
	state := s.states[key]
	if state == nil {
		state = &weightedRoundRobinState{}
		s.states[key] = state
	}
	weights := authWeightVector(available)
	if state.current == nil || !weightVectorsEqual(state.weights, weights) {
		state.current = make(map[string]int64)
	}
	state.weights = weights

	active := make(map[string]struct{}, len(available))
	var picked *Auth
	var pickedCurrent int64
	var totalWeight int64
	for _, candidate := range available {
		weight := authWeight(candidate)
		if candidate == nil || weight <= 0 {
			continue
		}
		active[candidate.ID] = struct{}{}
		state.current[candidate.ID] = saturatingAddInt64(state.current[candidate.ID], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		if picked == nil || state.current[candidate.ID] > pickedCurrent {
			picked = candidate
			pickedCurrent = state.current[candidate.ID]
		}
	}
	for authID := range state.current {
		if _, ok := active[authID]; !ok {
			delete(state.current, authID)
		}
	}
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no positive-weight auth available"}
	}
	state.current[picked.ID] = saturatingAddInt64(state.current[picked.ID], -totalWeight)
	return picked, nil
}

func authWeightVector(auths []*Auth) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if weight := authWeight(auth); weight > 0 {
			weights[auth.ID] = weight
		}
	}
	return weights
}

func weightVectorsEqual(left, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for authID, weight := range left {
		if right[authID] != weight {
			return false
		}
	}
	return true
}

func saturatingAddInt64(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}
