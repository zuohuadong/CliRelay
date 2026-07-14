package management

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	pscpu "github.com/shirou/gopsutil/v3/cpu"
	psdisk "github.com/shirou/gopsutil/v3/disk"
	psmem "github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
	psproc "github.com/shirou/gopsutil/v3/process"
)

// SystemStats is the JSON payload pushed via WebSocket and returned by HTTP.
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

// network baseline for rate calculation
var (
	netMu         sync.Mutex
	lastNetSample time.Time
	lastBytesSent uint64
	lastBytesRecv uint64
)

func (h *Handler) collectSystemStats() SystemStats {
	stats, _ := h.collectSystemStatsContext(context.Background())
	return stats
}

func (h *Handler) collectSystemStatsContext(ctx context.Context) (SystemStats, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	slowMetrics, err := h.loadSystemStatsSlowMetrics(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return SystemStats{}, err
		}
		slowMetrics = gin.H{}
	}
	stats.DBSizeBytes = int64FromGinValue(slowMetrics["db_size_bytes"])
	stats.LogDirSizeBytes = int64FromGinValue(slowMetrics["log_dir_size_bytes"])
	stats.LogSizeBytes = stats.LogDirSizeBytes
	if channelLatency, ok := slowMetrics["channel_latency"].([]map[string]interface{}); ok {
		stats.ChannelLatency = channelLatency
	}

	// Go runtime memory
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	stats.GoHeapBytes = mem.HeapAlloc
	stats.ProcessMemBytes = mem.Sys

	// Process CPU/Memory (gopsutil)
	if proc, err := psproc.NewProcess(int32(os.Getpid())); err == nil {
		if pct, err := proc.CPUPercent(); err == nil {
			stats.ProcessCPUPct = pct
		}
		if memInfo, err := proc.MemoryInfo(); err == nil {
			stats.ProcessMemBytes = memInfo.RSS
		}
		if pct, err := proc.MemoryPercent(); err == nil {
			stats.ProcessMemPct = float64(pct)
		}
	}

	// System CPU
	if pcts, err := pscpu.Percent(0, false); err == nil && len(pcts) > 0 {
		stats.SystemCPUPct = pcts[0]
	}

	// System Memory
	if vm, err := psmem.VirtualMemory(); err == nil {
		stats.SystemMemTotal = vm.Total
		stats.SystemMemUsed = vm.Used
		stats.SystemMemPct = vm.UsedPercent
	}

	// Network I/O
	if counters, err := psnet.IOCounters(false); err == nil && len(counters) > 0 {
		total := counters[0]
		stats.NetBytesSent = total.BytesSent
		stats.NetBytesRecv = total.BytesRecv

		netMu.Lock()
		now := time.Now()
		if !lastNetSample.IsZero() {
			elapsed := now.Sub(lastNetSample).Seconds()
			if elapsed > 0 {
				stats.NetSendRate = float64(total.BytesSent-lastBytesSent) / elapsed
				stats.NetRecvRate = float64(total.BytesRecv-lastBytesRecv) / elapsed
			}
		}
		lastNetSample = now
		lastBytesSent = total.BytesSent
		lastBytesRecv = total.BytesRecv
		netMu.Unlock()
	}

	// Disk usage
	if du, err := psdisk.Usage("/"); err == nil {
		stats.DiskTotal = du.Total
		stats.DiskUsed = du.Used
		stats.DiskFree = du.Free
		stats.DiskPct = du.UsedPercent
	}

	// Concurrency snapshot from middleware
	stats.ActiveConcurrency, stats.TotalInFlight = middleware.GetConcurrencySnapshot()

	// Compute system-wide RPM and TPM totals
	var sysRPM int
	var sysTPM int64
	for _, snap := range stats.ActiveConcurrency {
		if rpm, ok := snap["rpm"]; ok {
			if v, ok := rpm.(int); ok {
				sysRPM += v
			}
		}
		if tpm, ok := snap["tpm"]; ok {
			if v, ok := tpm.(int64); ok {
				sysTPM += v
			}
		}
	}
	stats.TotalRPM = sysRPM
	stats.TotalTPM = sysTPM

	return stats, nil
}

func (h *Handler) loadSystemStatsSlowMetrics(ctx context.Context) (gin.H, error) {
	payload, err := h.loadUsageAggregateWithTTL(
		ctx,
		usageAggregateSystemStats,
		usageFilters{Days: 7},
		systemStatsSlowCacheTTL,
		func(buildContext context.Context) (gin.H, error) {
			if errContext := buildContext.Err(); errContext != nil {
				return nil, errContext
			}
			dbSizeBytes := int64(0)
			dbPath := h.apiKeysDBPath()
			if dbPath != "" {
				if info, statErr := os.Stat(dbPath); statErr == nil {
					dbSizeBytes = info.Size()
				}
				for _, suffix := range []string{"-wal", "-shm"} {
					if info, statErr := os.Stat(dbPath + suffix); statErr == nil {
						dbSizeBytes += info.Size()
					}
				}
			}

			logDirSizeBytes := int64(0)
			if h.logDir != "" {
				var dirErr error
				logDirSizeBytes, dirErr = dirSizeContext(buildContext, h.logDir)
				if dirErr != nil {
					return nil, dirErr
				}
			}

			channelLatency := []map[string]interface{}{}
			loader := h.channelLatencyLoader
			if loader == nil {
				loader = usage.GetChannelAvgLatencyContext
			}
			if entries, loadErr := loader(buildContext, 7); loadErr == nil {
				for _, entry := range entries {
					channelLatency = append(channelLatency, map[string]interface{}{
						"source": entry.Source,
						"count":  entry.Count,
						"avg_ms": entry.AvgMs,
					})
				}
			} else if loadErr != nil {
				return nil, loadErr
			}

			return gin.H{
				"db_size_bytes":      dbSizeBytes,
				"log_dir_size_bytes": logDirSizeBytes,
				"channel_latency":    channelLatency,
			}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (h *Handler) GetSystemStats(c *gin.Context) {
	stats, err := h.collectSystemStatsContext(c.Request.Context())
	if err != nil {
		if c.Request.Context().Err() == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to collect system stats"})
		}
		return
	}
	c.JSON(http.StatusOK, stats)
}

// dirSize calculates the total size of all files in a directory tree.
func dirSize(root string) int64 {
	size, _ := dirSizeContext(context.Background(), root)
	return size
}

func dirSizeContext(ctx context.Context, root string) (int64, error) {
	var size int64
	errWalk := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if errContext := ctx.Err(); errContext != nil {
			return errContext
		}
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size, errWalk
}
