package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cluster"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// handleReadyz reports whether this process is ready to receive traffic.
// Unlike /healthz (liveness), readiness fails when required dependencies are unavailable.
func (s *Server) handleReadyz(c *gin.Context) {
	if s != nil && s.draining.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":          "not_ready",
			"reason":          "draining",
			"in_flight_count": s.inFlightRequests.Load(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if s.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "config_not_loaded"})
		return
	}
	if cluster.Default().Enabled() {
		s.handleClusterReadyz(ctx, c)
		return
	}

	if dsn := strings.TrimSpace(s.cfg.Postgres.DSN); dsn != "" {
		if err := pingPostgres(ctx, dsn); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready", "reason": "postgres"})
			return
		}
	}

	c.Status(http.StatusNoContent)
}

func pingPostgres(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Second)
	return db.PingContext(ctx)
}
