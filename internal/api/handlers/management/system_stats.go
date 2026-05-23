package management

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
)

type SystemStats struct {
	DBSizeBytes          int64                    `json:"db_size_bytes"`
	LogContentStoreBytes int64                    `json:"log_content_store_bytes"`
	LogDirSizeBytes      int64                    `json:"log_dir_size_bytes"`
	LogSizeBytes         int64                    `json:"log_size_bytes"`
	ProcessMemBytes      uint64                   `json:"process_mem_bytes"`
	ProcessMemPct        float64                  `json:"process_mem_pct"`
	ProcessCPUPct        float64                  `json:"process_cpu_pct"`
	GoRoutines           int                      `json:"go_routines"`
	GoHeapBytes          uint64                   `json:"go_heap_bytes"`
	SystemCPUPct         float64                  `json:"system_cpu_pct"`
	SystemMemTotal       uint64                   `json:"system_mem_total"`
	SystemMemUsed        uint64                   `json:"system_mem_used"`
	SystemMemPct         float64                  `json:"system_mem_pct"`
	NetBytesSent         uint64                   `json:"net_bytes_sent"`
	NetBytesRecv         uint64                   `json:"net_bytes_recv"`
	NetSendRate          float64                  `json:"net_send_rate"`
	NetRecvRate          float64                  `json:"net_recv_rate"`
	DiskTotal            uint64                   `json:"disk_total"`
	DiskUsed             uint64                   `json:"disk_used"`
	DiskFree             uint64                   `json:"disk_free"`
	DiskPct              float64                  `json:"disk_pct"`
	UptimeSeconds        int64                    `json:"uptime_seconds"`
	StartTime            string                   `json:"start_time"`
	ChannelLatency       []map[string]interface{} `json:"channel_latency"`
	ActiveConcurrency    []map[string]interface{} `json:"active_concurrency"`
	TotalInFlight        int64                    `json:"total_in_flight"`
	TotalRPM             int                      `json:"total_rpm"`
	TotalTPM             int64                    `json:"total_tpm"`
}

func (h *Handler) collectSystemStats() SystemStats {
	startTime := h.startTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	stats := SystemStats{
		GoRoutines:        runtime.NumGoroutine(),
		StartTime:         startTime.UTC().Format(time.RFC3339),
		UptimeSeconds:     int64(time.Since(startTime).Seconds()),
		ChannelLatency:    []map[string]interface{}{},
		ActiveConcurrency: []map[string]interface{}{},
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	stats.GoHeapBytes = mem.HeapAlloc
	stats.ProcessMemBytes = mem.Sys

	if h.logDir != "" {
		stats.LogDirSizeBytes = dirSize(h.logDir)
		stats.LogSizeBytes = stats.LogDirSizeBytes
	}

	fillDiskStats(&stats)

	return stats
}

func (h *Handler) GetSystemStats(c *gin.Context) {
	c.JSON(http.StatusOK, h.collectSystemStats())
}

func dirSize(root string) int64 {
	var size int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}
