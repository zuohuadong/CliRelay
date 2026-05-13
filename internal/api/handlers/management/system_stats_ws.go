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
	CheckOrigin: func(*http.Request) bool { return true },
}

func (h *Handler) SystemStatsWebSocket(c *gin.Context) {
	conn, err := statsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Warnf("system-stats ws: upgrade failed: %v", err)
		return
	}
	defer func() {
		_ = conn.Close()
	}()

	interval := 3 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

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

	writeStats := func() bool {
		data, err := json.Marshal(h.collectSystemStats())
		if err != nil {
			return true
		}
		return conn.WriteMessage(websocket.TextMessage, data) == nil
	}
	if !writeStats() {
		return
	}

	for {
		select {
		case <-done:
			return
		case msg := <-clientMsg:
			var req struct {
				Interval int `json:"interval"`
			}
			if json.Unmarshal(msg, &req) == nil && req.Interval >= 1 && req.Interval <= 60 {
				ticker.Stop()
				interval = time.Duration(req.Interval) * time.Second
				ticker = time.NewTicker(interval)
			}
		case <-ticker.C:
			if !writeStats() {
				return
			}
		}
	}
}
