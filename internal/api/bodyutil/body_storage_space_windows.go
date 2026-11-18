//go:build windows

package bodyutil

import "golang.org/x/sys/windows"

func requestBodyCacheHasFreeSpace(dir string, requiredBytes int64) bool {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return false
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(path, &free, nil, nil); err != nil {
		return false
	}
	needed := uint64(requiredBytes) + uint64(bodyStorageMinFreeBytes)
	if needed < uint64(requiredBytes) {
		return false
	}
	return free >= needed
}
