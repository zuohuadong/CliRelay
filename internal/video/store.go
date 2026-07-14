package video

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Job is the durable routing record for one public video generation task.
type Job struct {
	ID          string
	UpstreamID  string
	Provider    string
	AuthID      string
	Model       string
	Status      string
	Progress    int
	ResultURL   string
	ObjectKey   string
	ContentType string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ResultUpdate contains mutable state learned while polling a video task.
type ResultUpdate struct {
	Status      string
	Progress    int
	ResultURL   string
	ObjectKey   string
	ContentType string
}

// Store persists video task routing in SQLite so polling survives restarts and
// multiple server processes sharing the same data volume.
type Store struct {
	db *sql.DB
}

func DatabasePathForConfig(configPath string) string {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), "data", "video.db")
}

func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("video store path is required")
	}
	dataDir := filepath.Dir(path)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create video data directory: %w", err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("harden video data directory: %w", err)
	}
	dbFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create video store: %w", err)
	}
	if errClose := dbFile.Close(); errClose != nil {
		return nil, fmt.Errorf("close video store bootstrap file: %w", errClose)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("harden video store: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open video store: %w", err)
	}
	// SQLite PRAGMAs such as busy_timeout are connection-scoped. Keep this
	// store on one pooled connection so every operation uses the hardened
	// connection configured by migrate.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err = store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("video store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping video store: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("video store is unavailable")
	}
	for _, pragma := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
	} {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure video store: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS video_jobs (
  id TEXT PRIMARY KEY,
  upstream_id TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT '',
  auth_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued',
  progress INTEGER NOT NULL DEFAULT 0,
  result_url TEXT NOT NULL DEFAULT '',
  object_key TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_video_jobs_upstream_id ON video_jobs(upstream_id);
CREATE INDEX IF NOT EXISTS idx_video_jobs_status ON video_jobs(status);
`)
	if err != nil {
		return fmt.Errorf("migrate video store: %w", err)
	}
	return nil
}

func (s *Store) Create(ctx context.Context, job Job) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("video store is unavailable")
	}
	job.ID = strings.TrimSpace(job.ID)
	job.UpstreamID = strings.TrimSpace(job.UpstreamID)
	job.Provider = strings.TrimSpace(job.Provider)
	job.AuthID = strings.TrimSpace(job.AuthID)
	job.Model = strings.TrimSpace(job.Model)
	if job.ID == "" || job.UpstreamID == "" || job.Provider == "" || job.AuthID == "" || job.Model == "" {
		return fmt.Errorf("video job id, upstream id, provider, auth id, and model are required")
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if strings.TrimSpace(job.Status) == "" {
		job.Status = "queued"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO video_jobs (
  id, upstream_id, provider, auth_id, model, status, progress,
  result_url, object_key, content_type, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  upstream_id = excluded.upstream_id,
  provider = excluded.provider,
  auth_id = excluded.auth_id,
  model = excluded.model,
  status = excluded.status,
  progress = excluded.progress,
  result_url = excluded.result_url,
  object_key = excluded.object_key,
  content_type = excluded.content_type,
  updated_at = excluded.updated_at
`, job.ID, job.UpstreamID, job.Provider, job.AuthID, job.Model,
		strings.TrimSpace(job.Status), job.Progress, strings.TrimSpace(job.ResultURL), strings.TrimSpace(job.ObjectKey),
		strings.TrimSpace(job.ContentType), job.CreatedAt.Format(time.RFC3339Nano), job.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create video job: %w", err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	if s == nil || s.db == nil {
		return Job{}, fmt.Errorf("video store is unavailable")
	}
	var job Job
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT id, upstream_id, provider, auth_id, model, status, progress,
       result_url, object_key, content_type, created_at, updated_at
FROM video_jobs
WHERE id = ?
`, strings.TrimSpace(id)).Scan(
		&job.ID, &job.UpstreamID, &job.Provider, &job.AuthID, &job.Model, &job.Status, &job.Progress,
		&job.ResultURL, &job.ObjectKey, &job.ContentType, &createdAt, &updatedAt,
	)
	if err != nil {
		return Job{}, err
	}
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return job, nil
}

func (s *Store) UpdateResult(ctx context.Context, id string, update ResultUpdate) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("video store is unavailable")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE video_jobs
SET status = CASE WHEN ? <> '' THEN ? ELSE status END,
    progress = ?,
    result_url = CASE WHEN ? <> '' THEN ? ELSE result_url END,
    object_key = CASE WHEN ? <> '' THEN ? ELSE object_key END,
    content_type = CASE WHEN ? <> '' THEN ? ELSE content_type END,
    updated_at = ?
WHERE id = ?
`, strings.TrimSpace(update.Status), strings.TrimSpace(update.Status), update.Progress,
		strings.TrimSpace(update.ResultURL), strings.TrimSpace(update.ResultURL),
		strings.TrimSpace(update.ObjectKey), strings.TrimSpace(update.ObjectKey),
		strings.TrimSpace(update.ContentType), strings.TrimSpace(update.ContentType),
		time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("update video job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}
