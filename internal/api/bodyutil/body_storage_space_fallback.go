//go:build !unix && !windows

package bodyutil

func requestBodyCacheHasFreeSpace(string, int64) bool {
	return false
}
