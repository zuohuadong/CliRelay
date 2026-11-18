package vision

import (
	"sync"
	"time"
)

// SessionKey uniquely identifies a session for image memory isolation.
// Must be a real session-level identifier — never fallback to auth.ID.
type SessionKey string

// ImageHash is the SHA256 hex of normalized image content.
type ImageHash string

// ImageSourceKind classifies where the image came from.
type ImageSourceKind string

const (
	ImageSourceUserUpload ImageSourceKind = "user_upload"
	ImageSourceRemoteURL  ImageSourceKind = "remote_url"
	ImageSourceToolShot   ImageSourceKind = "tool_screenshot"
	ImageSourceUnknown    ImageSourceKind = "unknown"
)

// ImageAvailability describes whether the current request carries the raw image.
type ImageAvailability string

const (
	ImageAvailableInline ImageAvailability = "inline_payload"        // base64 inline in current request
	ImageAvailableRemote ImageAvailability = "remote_url_in_payload" // accessible URL in current request
	ImageUnavailableNow  ImageAvailability = "unavailable_in_request"
)

// ImageSummary holds structured, trim-able image description.
type ImageSummary struct {
	Summary      string   // 1-3 sentence overall description
	OCRHints     []string // up to 5 key text/OCR fragments
	LayoutHints  []string // up to 5 layout/position hints
	DetailHints  []string // up to 8 supplementary detail hints
	LastQuestion string   // most recent question that triggered analysis
	Confidence   string   // "high" / "medium" / "low"
}

// ImageOccurrence records where this image appeared in the conversation.
type ImageOccurrence struct {
	TurnIndex  int
	MessageIdx int
	PartIdx    int
}

// ImageEntry represents one unique image tracked across turns.
type ImageEntry struct {
	Hash                    ImageHash
	StableOrdinal           int // stable across turns: Image #1, #2, ...
	SourceKind              ImageSourceKind
	FirstSeenTurn           int
	LastSeenTurn            int
	Occurrences             []ImageOccurrence
	Availability            ImageAvailability
	CurrentPayloadReachable bool // whether this turn carries the raw bytes
	Summary                 ImageSummary
	CreatedAt               time.Time
	LastAnalyzedAt          time.Time
	LastAccessAt            time.Time
	Revision                int
}

// SessionStore holds all image entries for one session.
type SessionStore struct {
	mu          sync.RWMutex
	entries     map[ImageHash]*ImageEntry
	order       []ImageHash // LRU order for eviction
	nextOrdinal int
	updatedAt   time.Time
}

// GlobalConfig governs registry-wide resource limits.
type GlobalConfig struct {
	MaxSessions            int
	MaxEntriesPerSession   int
	MaxDetailHintsPerImage int
	SessionTTL             time.Duration
	AnalyzerTimeout        time.Duration
}

func DefaultGlobalConfig() GlobalConfig {
	return GlobalConfig{
		MaxSessions:            1000,
		MaxEntriesPerSession:   100,
		MaxDetailHintsPerImage: 8,
		SessionTTL:             2 * time.Hour,
		AnalyzerTimeout:        30 * time.Second,
	}
}
