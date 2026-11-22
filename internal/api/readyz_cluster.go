package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
)

// readyzRuntimeDB returns the shared runtime pool; tests replace it.
var readyzRuntimeDB = usage.RuntimeDB

// handleClusterReadyz is readiness in cluster mode. The database is shared
// by every node, so the probe pings the runtime pool this node serves from
// instead of dialing a fresh connection on every call.
func (s *Server) handleClusterReadyz(ctx context.Context, c *gin.Context) {
	db := readyzRuntimeDB()
	if db == nil || db.PingContext(ctx) != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "postgres"})
		return
	}
	c.Status(http.StatusNoContent)
}
