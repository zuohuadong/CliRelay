package vision

import (
	"fmt"
	"strings"
)

// BuildShortPlaceholder returns a short placeholder for historical images
// in the payload that won't be expanded this turn.
func BuildShortPlaceholder(e *ImageEntry) string {
	return fmt.Sprintf("[Image #%d from previous turn]", e.StableOrdinal)
}

// BuildOmittedPlaceholder returns an even shorter variant when the image
// should be mentioned but not described.
func BuildOmittedPlaceholder(e *ImageEntry) string {
	return fmt.Sprintf("[Image #%d]", e.StableOrdinal)
}

// BuildRegistryNote constructs the current-turn text block that summarizes
// all historical images relevant to the current question.
// This is the "registry note" injected into the current user message.
func BuildRegistryNote(entries []*ImageEntry, targetOrdinal int) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[Image Registry]")

	if targetOrdinal > 0 {
		// Focused note — only describe the relevant image
		for _, e := range entries {
			if e.StableOrdinal == targetOrdinal {
				b.WriteString(buildSingleEntryNote(e))
			}
		}
	} else {
		// General note — list all images
		for _, e := range entries {
			b.WriteString(buildSingleEntryNote(e))
		}
		if targetOrdinal == 0 && len(entries) > 1 {
			b.WriteString("\n当前会话存在多张历史图片，若需要我详细分析某一张，请告诉我具体是哪张图（如“第一张”或“Image #1”）。")
		}
	}

	return b.String()
}

func buildSingleEntryNote(e *ImageEntry) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\nImage #%d", e.StableOrdinal))

	if e.Summary.Summary != "" {
		b.WriteString(": " + e.Summary.Summary)
	}

	if len(e.Summary.OCRHints) > 0 {
		b.WriteString(" 包含文字: ")
		b.WriteString(strings.Join(truncateSlice(e.Summary.OCRHints, 3), "; "))
	}

	if !e.CurrentPayloadReachable {
		b.WriteString(" (该图片原始内容未出现在当前请求中，以下信息来自之前缓存，可能不覆盖全部细节)")
	}

	return b.String()
}

// BuildAmbiguityNote returns a note when the user's reference is unclear.
func BuildAmbiguityNote(entries []*ImageEntry) string {
	var b strings.Builder
	b.WriteString("[Image Registry]\n")
	b.WriteString(fmt.Sprintf("当前会话存在 %d 张历史图片，但当前问题未明确指向哪一张。", len(entries)))
	b.WriteString("\n若需要精确回答，请说明“第几张图”或“Image #N”。\n")
	b.WriteString("已有图片: ")
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("Image #%d", e.StableOrdinal))
	}
	return b.String()
}

func truncateSlice(s []string, max int) []string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// RenderSummary returns a human-readable string of the ImageSummary for logging or debugging.
func RenderSummary(s ImageSummary) string {
	var parts []string
	if s.Summary != "" {
		parts = append(parts, s.Summary)
	}
	if len(s.OCRHints) > 0 {
		parts = append(parts, "OCR: "+strings.Join(s.OCRHints, "; "))
	}
	if len(s.LayoutHints) > 0 {
		parts = append(parts, "Layout: "+strings.Join(s.LayoutHints, "; "))
	}
	if len(s.DetailHints) > 0 {
		parts = append(parts, "Details: "+strings.Join(s.DetailHints, "; "))
	}
	return strings.Join(parts, " | ")
}
