package vision

import (
	"testing"
	"time"
)

func TestContextRegistry_RegisterAndLookup(t *testing.T) {
	r := NewContextRegistry()

	r.Register("session1", ImageEntry{
		ID:        "img1",
		DataURL:   "data:image/png;base64,abc",
		TurnIndex: 0,
	})
	r.Register("session1", ImageEntry{
		ID:        "img2",
		DataURL:   "data:image/png;base64,def",
		TurnIndex: 1,
	})

	entries := r.Lookup("session1")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].ID != "img1" {
		t.Errorf("expected first entry ID img1, got %s", entries[0].ID)
	}
	if entries[1].ID != "img2" {
		t.Errorf("expected second entry ID img2, got %s", entries[1].ID)
	}
}

func TestContextRegistry_LookupByIndex(t *testing.T) {
	r := NewContextRegistry()

	r.Register("session1", ImageEntry{ID: "img1", TurnIndex: 0})
	r.Register("session1", ImageEntry{ID: "img2", TurnIndex: 1})
	r.Register("session1", ImageEntry{ID: "img3", TurnIndex: 1})

	entries := r.LookupByIndex("session1", 1)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries at index 1, got %d", len(entries))
	}
}

func TestContextRegistry_Clear(t *testing.T) {
	r := NewContextRegistry()

	r.Register("session1", ImageEntry{ID: "img1"})
	r.Register("session2", ImageEntry{ID: "img2"})

	r.Clear("session1")

	if len(r.Lookup("session1")) != 0 {
		t.Error("session1 should be cleared")
	}
	if len(r.Lookup("session2")) != 1 {
		t.Error("session2 should still have entries")
	}
}

func TestContextRegistry_MaxPerSession(t *testing.T) {
	r := NewContextRegistry()
	r.SetMaxPerSession(3)

	for i := 0; i < 5; i++ {
		r.Register("session1", ImageEntry{ID: string(rune('a' + i))})
	}

	entries := r.Lookup("session1")
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (max per session), got %d", len(entries))
	}
}

func TestContextRegistry_ExpiredEntries(t *testing.T) {
	r := NewContextRegistry()
	r.SetMaxAge(100 * time.Millisecond)

	r.Register("session1", ImageEntry{ID: "img1"})

	// Should be available immediately
	if len(r.Lookup("session1")) != 1 {
		t.Fatal("entry should be available")
	}

	// Wait for expiry
	time.Sleep(150 * time.Millisecond)

	if len(r.Lookup("session1")) != 0 {
		t.Fatal("entry should be expired")
	}
}

func TestContextRegistry_NilReceiver(t *testing.T) {
	var r *ContextRegistry

	// All methods should be safe on nil receiver
	r.Register("s", ImageEntry{})
	r.Lookup("s")
	r.LookupByIndex("s", 0)
	r.Clear("s")
	r.PurgeExpired()
	r.SetMaxAge(time.Hour)
	r.SetMaxPerSession(10)
}

func TestContextRegistry_EmptySession(t *testing.T) {
	r := NewContextRegistry()
	if len(r.Lookup("nonexistent")) != 0 {
		t.Error("nonexistent session should return empty")
	}
}
