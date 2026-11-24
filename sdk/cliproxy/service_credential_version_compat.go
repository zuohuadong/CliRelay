package cliproxy

import coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

func acceptCredentialReload(incoming, existing *coreauth.Auth) bool {
	if incoming == nil {
		return false
	}
	if existing == nil {
		return true
	}
	inVersion := coreauth.CredentialVersion(incoming)
	exVersion := coreauth.CredentialVersion(existing)
	if exVersion > 0 && inVersion == 0 {
		return false
	}
	if exVersion > 0 && inVersion < exVersion {
		return false
	}
	if inVersion == exVersion {
		coreauth.PreserveRuntimeMetadata(incoming, existing)
	}
	return true
}
