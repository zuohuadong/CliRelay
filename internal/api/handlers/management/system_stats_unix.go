package management

import (
	"syscall"
)

func fillDiskStats(stats *SystemStats) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		blockSize := uint64(fs.Bsize)
		stats.DiskTotal = fs.Blocks * blockSize
		stats.DiskFree = fs.Bavail * blockSize
		if stats.DiskTotal >= stats.DiskFree {
			stats.DiskUsed = stats.DiskTotal - stats.DiskFree
		}
		if stats.DiskTotal > 0 {
			stats.DiskPct = float64(stats.DiskUsed) / float64(stats.DiskTotal) * 100
		}
	}
}
