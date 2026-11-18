package vision

import (
	"strings"
	"unicode/utf8"
)

// Intent describes what the processor should do with historical images.
type Intent int

const (
	IntentNone      Intent = iota // no historical image action needed
	IntentFollowUp                // user is asking about a specific image
	IntentAmbiguous               // user references image but target is unclear
	IntentNewImage                // current turn has new images
)

// DetectIntent examines the last user message and registry state to determine
// whether a follow-up image analysis should be triggered.
func DetectIntent(lastUserMessage string, historicalCount int) Intent {
	if historicalCount == 0 {
		return IntentNone
	}

	msg := strings.TrimSpace(lastUserMessage)
	if msg == "" {
		return IntentNone
	}

	// Check for explicit image reference
	if hasImageReference(msg) {
		return IntentFollowUp
	}

	// Check for visual follow-up patterns (only when candidate images ≤ 2)
	if historicalCount <= 2 && hasVisualFollowUpPattern(msg) {
		return IntentFollowUp
	}

	// Check for ambiguity
	if historicalCount > 1 && hasVisualFollowUpPattern(msg) {
		return IntentAmbiguous
	}

	return IntentNone
}

// ExtractImageReference extracts which image number is being referenced.
// Returns 0 if no clear reference is found.
func ExtractImageReference(msg string) int {
	// Check for "Image #N"
	lower := strings.ToLower(msg)
	idx := strings.Index(lower, "image #")
	if idx >= 0 {
		rest := lower[idx+7:]
		// Read the number after "#"
		num := 0
		for _, r := range rest {
			if r >= '0' && r <= '9' {
				num = num*10 + int(r-'0')
			} else {
				break
			}
		}
		if num > 0 {
			return num
		}
	}

	// Check for Chinese ordinal
	ordinals := []string{"第一", "第二", "第三", "第四", "第五"}
	for i, ord := range ordinals {
		if strings.Contains(lower, ord) || strings.Contains(msg, ord) {
			return i + 1
		}
	}

	return 0
}

func hasImageReference(msg string) bool {
	lower := strings.ToLower(msg)

	// "Image #N" pattern
	if strings.Contains(lower, "image #") {
		return true
	}

	// "图片" pattern
	if strings.Contains(msg, "图片") {
		return true
	}

	// "第X张" pattern (Chinese ordinal + 张)
	if strings.Contains(msg, "一张") || strings.Contains(msg, "二张") ||
		strings.Contains(msg, "三张") || strings.Contains(msg, "四张") ||
		strings.Contains(msg, "五张") || strings.Contains(msg, "张图") {
		return true
	}

	// "这张" "那张" "哪张" — pronouns for specific images
	if strings.Contains(msg, "这张") || strings.Contains(msg, "那张") || strings.Contains(msg, "哪张") {
		return true
	}

	return false
}

func hasVisualFollowUpPattern(msg string) bool {
	// Must be a question or contain visual reference keywords
	if !isQuestion(msg) {
		return false
	}

	// Short messages (1-3 chars) are unlikely to be visual follow-ups
	if utf8.RuneCountInString(msg) < 4 {
		return false
	}

	keywords := []string{
		"什么", "哪里", "哪个", "哪些",
		"细节", "更多", "还有", "再", "另外",
		"图标", "按钮", "颜色", "文字",
		"里面", "上面", "下面", "右边", "左边",
		"弹窗", "菜单", "工具栏", "状态",
		"错误", "报错", "日志",
		"different", "detail", "more", "another",
		"icon", "button", "color", "text",
		"left", "right", "top", "bottom", "corner",
	}

	lower := strings.ToLower(msg)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	return false
}

func isQuestion(msg string) bool {
	msg = strings.TrimSpace(msg)

	// Question markers at end
	if strings.HasSuffix(msg, "吗") ||
		strings.HasSuffix(msg, "呢") ||
		strings.HasSuffix(msg, "？") ||
		strings.HasSuffix(msg, "?") {
		return true
	}

	// Question words anywhere in the message
	questionWords := []string{
		"什么", "怎么", "为什么", "哪里", "哪个", "哪些",
		"是否", "有没有", "能不能", "会不会",
		"what", "how", "why", "where", "which",
	}
	lower := strings.ToLower(msg)
	for _, w := range questionWords {
		if strings.Contains(lower, w) || strings.Contains(msg, w) {
			return true
		}
	}

	return false
}
