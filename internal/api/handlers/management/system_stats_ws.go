package management

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

var statsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// SystemStatsWebSocket handles GET /v0/management/system-stats/ws
// It pushes SystemStats JSON at a configurable interval (default 3s).
// The client may send {"interval": <seconds>} to adjust the push interval.
func (h *Handler) SystemStatsWebSocket(c *gin.Context) {
	conn, err := statsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Warnf("system-stats ws: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	interval := 3 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Background reader: listen for client messages to adjust interval
	clientMsg := make(chan json.RawMessage, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case clientMsg <- json.RawMessage(msg):
			default:
			}
		}
	}()

	// Send initial stats immediately
	if data, err := json.Marshal(h.collectSystemStats()); err == nil {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}

	for {
		select {
		case <-done:
			return
		case msg := <-clientMsg:
			// Parse interval change request
			var req struct {
				Interval int `json:"interval"`
			}
			if json.Unmarshal(msg, &req) == nil && req.Interval >= 1 && req.Interval <= 60 {
				ticker.Stop()
				interval = time.Duration(req.Interval) * time.Second
				ticker = time.NewTicker(interval)
				log.Infof("system-stats ws: interval changed to %ds", req.Interval)
			}
		case <-ticker.C:
			stats := h.collectSystemStats()
			data, err := json.Marshal(stats)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}
