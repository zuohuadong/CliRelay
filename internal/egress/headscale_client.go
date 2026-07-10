package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type Enrollment struct {
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
	Command   string    `json:"command"`
}

type HeadscaleClient struct {
	cfg       config.HeadscaleConfig
	http      *http.Client
	lookupEnv func(string) string
}

var enrollmentHostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func NewHeadscaleClient(cfg config.HeadscaleConfig) *HeadscaleClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &HeadscaleClient{
		cfg:       cfg,
		http:      &http.Client{Transport: transport, Timeout: 15 * time.Second},
		lookupEnv: func(name string) string { return strings.TrimSpace(os.Getenv(name)) },
	}
}

func (c *HeadscaleClient) Configured() bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.cfg.URL) != "" && strings.TrimSpace(c.apiKey()) != ""
}

func (c *HeadscaleClient) ListNodes(ctx context.Context) ([]Node, error) {
	var response struct {
		Nodes []struct {
			ID          string    `json:"id"`
			Name        string    `json:"name"`
			GivenName   string    `json:"givenName"`
			IPAddresses []string  `json:"ipAddresses"`
			Online      bool      `json:"online"`
			LastSeen    time.Time `json:"lastSeen"`
			Tags        []string  `json:"tags"`
		} `json:"nodes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/node", nil, &response); err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(response.Nodes))
	for _, item := range response.Nodes {
		name := strings.TrimSpace(item.GivenName)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		nodes = append(nodes, Node{
			ID:        strings.TrimSpace(item.ID),
			Name:      name,
			Addresses: append([]string(nil), item.IPAddresses...),
			Online:    item.Online,
			LastSeen:  item.LastSeen,
			Tags:      append([]string(nil), item.Tags...),
		})
	}
	return nodes, nil
}

func (c *HeadscaleClient) CreateEnrollment(ctx context.Context, name string, expiresAt time.Time) (Enrollment, error) {
	if expiresAt.IsZero() {
		return Enrollment{}, fmt.Errorf("enrollment expiration is required")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" && !enrollmentHostnamePattern.MatchString(name) {
		return Enrollment{}, fmt.Errorf("invalid enrollment hostname")
	}
	tag := strings.TrimSpace(c.cfg.ServiceTag)
	if tag == "" {
		tag = config.DefaultEgressServiceTag
	}
	body := map[string]any{
		"reusable":   false,
		"ephemeral":  false,
		"expiration": expiresAt.UTC().Format(time.RFC3339),
		"aclTags":    []string{tag},
	}
	var response struct {
		PreAuthKey struct {
			Key        string    `json:"key"`
			Expiration time.Time `json:"expiration"`
		} `json:"preAuthKey"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/preauthkey", body, &response); err != nil {
		return Enrollment{}, err
	}
	key := strings.TrimSpace(response.PreAuthKey.Key)
	if key == "" {
		return Enrollment{}, fmt.Errorf("headscale returned an empty preauth key")
	}
	args := []string{
		"tailscale", "up",
		"--login-server=" + strings.TrimRight(strings.TrimSpace(c.cfg.URL), "/"),
		"--auth-key=" + key,
		"--advertise-tags=" + tag,
	}
	if name != "" {
		args = append(args, "--hostname="+name)
	}
	return Enrollment{
		Key:       key,
		ExpiresAt: response.PreAuthKey.Expiration,
		Command:   shellJoin(args),
	}, nil
}

func (c *HeadscaleClient) doJSON(ctx context.Context, method, path string, body any, target any) error {
	if c == nil || c.http == nil {
		return fmt.Errorf("headscale client is unavailable")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(c.cfg.URL), "/")
	apiKey := strings.TrimSpace(c.apiKey())
	if baseURL == "" || apiKey == "" {
		return fmt.Errorf("headscale is not configured")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("headscale request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("headscale request failed with status %d", resp.StatusCode)
	}
	if target != nil && len(responseBody) > 0 {
		if err = json.Unmarshal(responseBody, target); err != nil {
			return fmt.Errorf("decode headscale response: %w", err)
		}
	}
	return nil
}

func (c *HeadscaleClient) apiKey() string {
	if c == nil || c.lookupEnv == nil {
		return ""
	}
	return strings.TrimSpace(c.lookupEnv(strings.TrimSpace(c.cfg.APIKeyEnv)))
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("-._/:=", r)
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
