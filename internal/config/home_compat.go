package config

// NormalizeHomePort applies the Home control-plane default to an unset port.
func NormalizeHomePort(port int) int {
	if port <= 0 {
		return 8317
	}
	return port
}
