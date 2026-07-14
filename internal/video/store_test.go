package video

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorePersistsVideoJobAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "video.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	want := Job{
		ID:         "video_public_123",
		UpstreamID: "task_upstream_456",
		Provider:   "agnes",
		AuthID:     "auth_789",
		Model:      "agnes-video-v2.0",
		Status:     "queued",
	}
	if err = store.Create(context.Background(), want); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() after close error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	got, err := reopened.Get(context.Background(), want.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != want.ID || got.UpstreamID != want.UpstreamID || got.Provider != want.Provider || got.AuthID != want.AuthID || got.Model != want.Model || got.Status != want.Status {
		t.Fatalf("Get() = %+v, want persisted routing fields %+v", got, want)
	}
}

func TestStoreUpdatesCompletedVideoObject(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	job := Job{ID: "video_public_123", UpstreamID: "upstream_123", Provider: "xai", AuthID: "auth_123", Model: "grok-imagine-video", Status: "queued"}
	if err = store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err = store.UpdateResult(context.Background(), job.ID, ResultUpdate{
		Status:      "completed",
		Progress:    100,
		ResultURL:   "https://upstream.example/video.mp4",
		ObjectKey:   "generated/video_public_123.mp4",
		ContentType: "video/mp4",
	}); err != nil {
		t.Fatalf("UpdateResult() error = %v", err)
	}

	got, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != "completed" || got.Progress != 100 || got.ResultURL == "" || got.ObjectKey != "generated/video_public_123.mp4" || got.ContentType != "video/mp4" {
		t.Fatalf("updated job = %+v", got)
	}
}

func TestStoreRejectsJobWithoutDurableCredentialRouting(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "video.db"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.Create(context.Background(), Job{
		ID:         "video_public_123",
		UpstreamID: "upstream_123",
		Provider:   "xai",
		Model:      "grok-imagine-video",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want missing auth id rejection")
	}
}
