package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func atomicReplaceConfig(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	tmpPath := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		t.Fatalf("write temp: %v", err)
	}
	if err = tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
}

func startConfigEventCounter(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\nport: 8317\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new fsnotify watcher: %v", err)
	}
	if err = fsWatcher.Add(filepath.Dir(configPath)); err != nil {
		t.Fatalf("watch config directory: %v", err)
	}
	w := &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		watcher:        fsWatcher,
		lastAuthHashes: make(map[string]string),
		reloadCallback: func(*config.Config) {},
	}
	w.SetConfig(&config.Config{AuthDir: authDir})

	var counter atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = fsWatcher.Close()
	})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-fsWatcher.Events:
				if !ok {
					return
				}
				if w.normalizeAuthPath(event.Name) == w.normalizeAuthPath(configPath) {
					counter.Add(1)
				}
				w.handleEvent(event)
			case _, ok := <-fsWatcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return configPath, &counter
}

func awaitConfigEventIncrease(t *testing.T, events *atomic.Int32, baseline int32, message string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if events.Load() > baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: config event count stuck at %d", message, baseline)
}

func TestConfigWatchSurvivesRepeatedAtomicReplace(t *testing.T) {
	configPath, events := startConfigEventCounter(t)
	authDir := filepath.Join(filepath.Dir(configPath), "auth")
	for i := 1; i <= 3; i++ {
		baseline := events.Load()
		atomicReplaceConfig(t, configPath, fmt.Appendf(nil, "auth_dir: %s\nport: %d\n", authDir, 8317+i))
		awaitConfigEventIncrease(t, events, baseline, fmt.Sprintf("atomic replace #%d", i))
	}
}

func TestConfigWatchNoticesInPlaceWriteAfterAtomicReplace(t *testing.T) {
	configPath, events := startConfigEventCounter(t)
	authDir := filepath.Join(filepath.Dir(configPath), "auth")
	baseline := events.Load()
	atomicReplaceConfig(t, configPath, fmt.Appendf(nil, "auth_dir: %s\nport: 8318\n", authDir))
	awaitConfigEventIncrease(t, events, baseline, "atomic replace")

	baseline = events.Load()
	if err := os.WriteFile(configPath, fmt.Appendf(nil, "auth_dir: %s\nport: 8319\n", authDir), 0o600); err != nil {
		t.Fatalf("in-place write: %v", err)
	}
	awaitConfigEventIncrease(t, events, baseline, "in-place write after atomic replace")
}

func TestHandleEventSchedulesReloadForConfigContentOps(t *testing.T) {
	for _, op := range []fsnotify.Op{fsnotify.Remove, fsnotify.Rename, fsnotify.Write, fsnotify.Create} {
		t.Run(op.String(), func(t *testing.T) {
			w, configPath := newConfigHandleEventWatcher(t)
			w.handleEvent(fsnotify.Event{Name: configPath, Op: op})
			if timer := takeConfigReloadTimer(w); timer == nil {
				t.Fatalf("%s on config path did not schedule a reload", op)
			}
		})
	}
}

func TestHandleEventDoesNotScheduleReloadForConfigChmod(t *testing.T) {
	w, configPath := newConfigHandleEventWatcher(t)
	w.handleEvent(fsnotify.Event{Name: configPath, Op: fsnotify.Chmod})
	if timer := takeConfigReloadTimer(w); timer != nil {
		t.Fatal("chmod on config path scheduled a reload")
	}
}

func newConfigHandleEventWatcher(t *testing.T) (*Watcher, string) {
	t.Helper()
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("auth_dir: "+authDir+"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("new fsnotify watcher: %v", err)
	}
	t.Cleanup(func() { _ = fsWatcher.Close() })
	if err = fsWatcher.Add(filepath.Dir(configPath)); err != nil {
		t.Fatalf("watch config directory: %v", err)
	}
	return &Watcher{
		authDir:        authDir,
		configPath:     configPath,
		watcher:        fsWatcher,
		lastAuthHashes: make(map[string]string),
	}, configPath
}

func takeConfigReloadTimer(w *Watcher) *time.Timer {
	w.configReloadMu.Lock()
	defer w.configReloadMu.Unlock()
	timer := w.configReloadTimer
	if timer != nil {
		timer.Stop()
		w.configReloadTimer = nil
	}
	return timer
}
