package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func marshalAuthMetadata(auth *cliproxyauth.Auth) ([]byte, error) {
	auth.Metadata["disabled"] = auth.Disabled
	raw, err := json.Marshal(auth.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal auth metadata: %w", err)
	}
	return raw, nil
}

func authMetadataFileMatches(path string, raw []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil {
		return jsonEqual(existing, raw), nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("read existing auth metadata: %w", err)
}

func tightenAuthMetadataIfMatching(path string, raw []byte) (bool, error) {
	matches, err := authMetadataFileMatches(path, raw)
	if err != nil || !matches {
		return matches, err
	}
	if err = misc.TightenCredentialFilePermissions(path); err != nil {
		return false, fmt.Errorf("tighten existing auth metadata permissions: %w", err)
	}
	return true, nil
}
