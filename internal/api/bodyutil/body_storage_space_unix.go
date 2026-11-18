//go:build unix

package bodyutil

import "golang.org/x/sys/unix"

func requestBodyCacheHasFreeSpace(dir string, requiredBytes int64) bool {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return false
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	return available-requiredBytes >= bodyStorageMinFreeBytes
}
