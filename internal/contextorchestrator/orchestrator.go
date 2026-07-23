// Package contextorchestrator provides protocol-aware, deterministic context compaction.
// LLMs only produce a structured memory capsule; request JSON is always rebuilt locally.
package contextorchestrator

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"

	"github.com/tidwall/sjson"
)

const CapsuleVersion = "1"

// Capsule is the only structure an external compression model may produce.
// Protocol fields, tools, IDs and recent turns never pass through this structure.
type Capsule struct {
	Version       string   `json:"version,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Goals         []string `json:"goals,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	Decisions     []string `json:"decisions,omitempty"`
	Facts         []string `json:"verified_facts,omitempty"`
	Artifacts     []string `json:"artifacts,omitempty"`
	ToolResults   []string `json:"resolved_tool_results,omitempty"`
	OpenLoops     []string `json:"open_loops,omitempty"`
	Media         []string `json:"media_observations,omitempty"`
	Uncertainties []string `json:"uncertainties,omitempty"`
}

// SourceItem is the reduced, untrusted history passed to the compression model.
type SourceItem struct {
	Ordinal int    `json:"ordinal"`
	Role    string `json:"role,omitempty"`
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
}

// MediaRef is an image reference that may be forwarded to a multimodal compressor.
type MediaRef struct {
	Ordinal int    `json:"ordinal"`
	Kind    string `json:"kind"`
	URL     string `json:"url"`
}

type candidate struct {
	itemIndex int
	raw       json.RawMessage
	source    SourceItem
	media     []MediaRef
}

type mediaScan struct {
	refs        []MediaRef
	hasMedia    bool
	unsupported bool
}

// Plan describes which old history items may be replaced by one capsule.
// All raw request data remains private to the plan and is never cached by this package.
type Plan struct {
	raw          []byte
	field        string
	sourceFormat string
	items        []json.RawMessage
	candidates   []candidate
}

// BuildTextPlan keeps media pinned and is used for text-only compressors.
func BuildTextPlan(raw []byte, sourceFormat string, preserveRecentItems int) (*Plan, error) {
	return buildPlan(planBuildRequest{raw: raw, sourceFormat: sourceFormat, preserveRecentItems: preserveRecentItems})
}

// BuildMultimodalPlan permits old image items to be represented by a capsule.
func BuildMultimodalPlan(raw []byte, sourceFormat string, preserveRecentItems int) (*Plan, error) {
	return buildPlan(planBuildRequest{raw: raw, sourceFormat: sourceFormat, preserveRecentItems: preserveRecentItems, allowImageCompression: true})
}

type planBuildRequest struct {
	raw                   []byte
	sourceFormat          string
	preserveRecentItems   int
	allowImageCompression bool
}

func buildPlan(request planBuildRequest) (*Plan, error) {
	field, historyItems, err := validatedHistory(request.raw)
	if err != nil {
		return nil, err
	}
	plan := newPlan(request.raw, request.sourceFormat, field, historyItems)
	recentStart := recentHistoryStart(len(historyItems), request.preserveRecentItems)
	selection := candidateSelection{recentStart: recentStart, latestUser: latestUserIndex(historyItems), allowImageCompression: request.allowImageCompression, resolvedToolItems: resolvedToolItemIndexes(historyItems, recentStart)}
	for historyIndex, historyRaw := range historyItems {
		if historyCandidate, ok := buildCandidate(historyIndex, historyRaw, selection); ok {
			plan.candidates = append(plan.candidates, historyCandidate)
		}
	}
	if len(plan.candidates) == 0 {
		return nil, fmt.Errorf("request has no safely compressible historical items")
	}
	return plan, nil
}

func validatedHistory(raw []byte) (string, []json.RawMessage, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return "", nil, fmt.Errorf("context compaction requires a valid JSON request")
	}
	field, historyItems, err := extractItems(raw)
	if err != nil {
		return "", nil, err
	}
	if field == "" || len(historyItems) < 2 {
		return "", nil, fmt.Errorf("context compaction requires an array field named input, messages, or contents")
	}
	return field, historyItems, nil
}

func newPlan(raw []byte, sourceFormat, field string, historyItems []json.RawMessage) *Plan {
	return &Plan{raw: append([]byte(nil), raw...), field: field, sourceFormat: strings.ToLower(strings.TrimSpace(sourceFormat)), items: cloneRawMessages(historyItems)}
}

type candidateSelection struct {
	recentStart           int
	latestUser            int
	allowImageCompression bool
	resolvedToolItems     map[int]struct{}
}

func recentHistoryStart(historyLength, preserveRecentItems int) int {
	if preserveRecentItems <= 0 {
		preserveRecentItems = 8
	}
	return maxInt(historyLength-preserveRecentItems, 0)
}

func latestUserIndex(historyItems []json.RawMessage) int {
	for index := len(historyItems) - 1; index >= 0; index-- {
		if roleOf(historyItems[index]) == "user" {
			return index
		}
	}
	return -1
}

func buildCandidate(historyIndex int, historyRaw json.RawMessage, selection candidateSelection) (candidate, bool) {
	role := roleOf(historyRaw)
	_, resolvedToolItem := selection.resolvedToolItems[historyIndex]
	if candidatePinned(historyIndex, historyRaw, selection) {
		return candidate{}, false
	}
	media := scanMediaRefs(historyRaw)
	if !mediaCompressible(media, selection.allowImageCompression) {
		return candidate{}, false
	}
	for mediaIndex := range media.refs {
		media.refs[mediaIndex].Ordinal = historyIndex
	}
	text := extractText(historyRaw)
	if resolvedToolItem {
		text = string(historyRaw)
	}
	if strings.TrimSpace(text) == "" && len(media.refs) == 0 {
		return candidate{}, false
	}
	return candidate{itemIndex: historyIndex, raw: append(json.RawMessage(nil), historyRaw...), source: SourceItem{Ordinal: historyIndex, Role: role, Type: typeOf(historyRaw), Text: text}, media: media.refs}, true
}

func candidatePinned(historyIndex int, historyRaw json.RawMessage, selection candidateSelection) bool {
	role := roleOf(historyRaw)
	_, resolvedToolItem := selection.resolvedToolItems[historyIndex]
	return historyIndex >= selection.recentStart || historyIndex == selection.latestUser || role == "system" || role == "developer" || (protectedItem(historyRaw) && !resolvedToolItem)
}

func mediaCompressible(media mediaScan, allowImageCompression bool) bool {
	return !media.hasMedia || (allowImageCompression && !media.unsupported && !containsNonImageMedia(media.refs))
}

type toolPairIndexes struct {
	call   int
	output int
}

func resolvedToolItemIndexes(historyItems []json.RawMessage, recentStart int) map[int]struct{} {
	pairs := make(map[string]toolPairIndexes)
	for historyIndex, historyRaw := range historyItems[:recentStart] {
		callID, itemType := toolCallIdentity(historyRaw)
		if callID == "" {
			continue
		}
		pair := pairs[callID]
		if itemType == "function_call" {
			pair.call = historyIndex + 1
		} else if itemType == "function_call_output" {
			pair.output = historyIndex + 1
		}
		pairs[callID] = pair
	}
	return completeToolPairIndexes(pairs)
}

func toolCallIdentity(raw json.RawMessage) (string, string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", ""
	}
	var callID, itemType string
	_ = json.Unmarshal(fields["call_id"], &callID)
	_ = json.Unmarshal(fields["type"], &itemType)
	return strings.TrimSpace(callID), strings.ToLower(strings.TrimSpace(itemType))
}

func completeToolPairIndexes(pairs map[string]toolPairIndexes) map[int]struct{} {
	complete := make(map[int]struct{}, len(pairs)*2)
	for _, pair := range pairs {
		if pair.call <= 0 || pair.output <= 0 {
			continue
		}
		complete[pair.call-1] = struct{}{}
		complete[pair.output-1] = struct{}{}
	}
	return complete
}

func maxInt(first, second int) int {
	if first > second {
		return first
	}
	return second
}

func extractItems(raw []byte) (string, []json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", nil, err
	}
	for _, field := range []string{"input", "messages", "contents"} {
		rawHistory, ok := root[field]
		if !ok || len(rawHistory) == 0 || string(rawHistory) == "null" {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(rawHistory, &items); err == nil {
			return field, items, nil
		}
	}
	return "", nil, nil
}

func cloneRawMessages(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i := range in {
		out[i] = append(json.RawMessage(nil), in[i]...)
	}
	return out
}

// CandidateCount returns the number of old items represented by the resulting capsule.
func (p *Plan) CandidateCount() int {
	if p == nil {
		return 0
	}
	return len(p.candidates)
}

// Field returns the protocol history field selected by the plan.
func (p *Plan) Field() string {
	if p == nil {
		return ""
	}
	return p.field
}

// PrefixDigests returns a digest for every candidate prefix. Index n-1 represents
// candidates [0:n], allowing a later relay request to find its longest cached prefix.
func (p *Plan) PrefixDigests() []string {
	if p == nil || len(p.candidates) == 0 {
		return nil
	}
	h := sha256.New()
	writeDigestPart(h, p.sourceFormat)
	writeDigestPart(h, p.field)
	out := make([]string, 0, len(p.candidates))
	for _, historyCandidate := range p.candidates {
		writeDigestPart(h, fmt.Sprintf("%d", historyCandidate.itemIndex))
		_, _ = h.Write(historyCandidate.raw)
		_, _ = h.Write([]byte{0})
		out = append(out, hex.EncodeToString(h.Sum(nil)))
	}
	return out
}

func writeDigestPart(h hash.Hash, digestPart string) {
	_, _ = h.Write([]byte(digestPart))
	_, _ = h.Write([]byte{0})
}

// SourceItems returns only the uncached delta that the compression model needs to read.
func (p *Plan) SourceItems(start int) []SourceItem {
	if p == nil {
		return nil
	}
	start = normalizedStart(start, len(p.candidates))
	if start >= len(p.candidates) {
		return nil
	}
	out := make([]SourceItem, 0, len(p.candidates)-start)
	for _, historyCandidate := range p.candidates[start:] {
		out = append(out, historyCandidate.source)
	}
	return out
}

// MediaRefs returns image inputs belonging only to the uncached delta.
func (p *Plan) MediaRefs(start int) []MediaRef {
	if p == nil {
		return nil
	}
	start = normalizedStart(start, len(p.candidates))
	if start >= len(p.candidates) {
		return nil
	}
	seen := map[string]struct{}{}
	var out []MediaRef
	for _, historyCandidate := range p.candidates[start:] {
		for _, ref := range historyCandidate.media {
			key := ref.Kind + "\x00" + ref.URL
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ref)
		}
	}
	return out
}

func normalizedStart(start, length int) int {
	if start < 0 {
		return 0
	}
	if start > length {
		return length
	}
	return start
}

// Assemble replaces candidate items with a single protocol-compatible capsule item.
// Every non-candidate byte-level JSON item is copied from the current request.
func (p *Plan) Assemble(capsule Capsule) ([]byte, error) {
	if p == nil || len(p.candidates) == 0 {
		return nil, fmt.Errorf("context compaction plan is empty")
	}
	capsule = NormalizeCapsule(capsule)
	if !capsule.Valid() {
		return nil, fmt.Errorf("context compressor returned an empty memory capsule")
	}
	summaryItem, err := capsuleItem(p.field, p.sourceFormat, capsule)
	if err != nil {
		return nil, err
	}
	selectedHistory := p.compactedHistory(summaryItem)
	return p.replaceHistory(selectedHistory)
}

func (p *Plan) compactedHistory(summaryItem json.RawMessage) []json.RawMessage {
	candidateIndexes := make(map[int]struct{}, len(p.candidates))
	for _, historyCandidate := range p.candidates {
		candidateIndexes[historyCandidate.itemIndex] = struct{}{}
	}
	first := p.candidates[0].itemIndex
	selected := make([]json.RawMessage, 0, len(p.items)-len(p.candidates)+1)
	for historyIndex, historyRaw := range p.items {
		if historyIndex == first {
			selected = append(selected, summaryItem)
		}
		if _, compacted := candidateIndexes[historyIndex]; compacted {
			continue
		}
		selected = append(selected, append(json.RawMessage(nil), historyRaw...))
	}
	return selected
}

func (p *Plan) replaceHistory(selectedHistory []json.RawMessage) ([]byte, error) {
	historyJSON, err := json.Marshal(selectedHistory)
	if err != nil {
		return nil, err
	}
	compactedRequest, err := sjson.SetRawBytes(p.raw, p.field, historyJSON)
	if err != nil {
		return nil, err
	}
	if !json.Valid(compactedRequest) {
		return nil, fmt.Errorf("deterministic context assembly produced invalid JSON")
	}
	return compactedRequest, nil
}

func capsuleItem(field, sourceFormat string, capsule Capsule) (json.RawMessage, error) {
	payload, err := json.Marshal(capsule)
	if err != nil {
		return nil, err
	}
	text := "[compacted_context]\n" + string(payload)
	return json.Marshal(summaryItemShape(field, sourceFormat, text))
}

func summaryItemShape(field, sourceFormat, text string) any {
	switch field {
	case "contents":
		return map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}
	case "messages":
		if sourceFormat == "claude" {
			return map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}
		}
		return map[string]any{"role": "user", "content": text}
	default:
		return map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": text}},
		}
	}
}

// ParseCapsule parses strict structured output from a compressor response text.
func ParseCapsule(text string) (Capsule, error) {
	raw, err := capsuleJSON(text)
	if err != nil {
		return Capsule{}, err
	}
	var capsule Capsule
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capsule); err != nil {
		return Capsule{}, fmt.Errorf("context compressor returned invalid capsule: %w", err)
	}
	capsule = NormalizeCapsule(capsule)
	if capsule.Version != CapsuleVersion {
		return Capsule{}, fmt.Errorf("context compressor returned unsupported capsule version %q", capsule.Version)
	}
	if !capsule.Valid() {
		return Capsule{}, fmt.Errorf("context compressor returned an empty memory capsule")
	}
	return capsule, nil
}

func capsuleJSON(text string) ([]byte, error) {
	trimmed := stripMarkdownJSON(text)
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return nil, fmt.Errorf("context compressor returned non-JSON capsule: %w", err)
	}
	if wrapped, ok := object["memory_capsule"]; ok && len(object) == 1 && len(wrapped) > 0 && string(wrapped) != "null" {
		return wrapped, nil
	}
	return []byte(trimmed), nil
}

// NormalizeCapsule makes cache entries and rendered output stable.
func NormalizeCapsule(c Capsule) Capsule {
	c.Version = strings.TrimSpace(c.Version)
	if c.Version == "" {
		c.Version = CapsuleVersion
	}
	c.Summary = strings.TrimSpace(c.Summary)
	c.Goals = normalizeStrings(c.Goals)
	c.Constraints = normalizeStrings(c.Constraints)
	c.Decisions = normalizeStrings(c.Decisions)
	c.Facts = normalizeStrings(c.Facts)
	c.Artifacts = normalizeStrings(c.Artifacts)
	c.ToolResults = normalizeStrings(c.ToolResults)
	c.OpenLoops = normalizeStrings(c.OpenLoops)
	c.Media = normalizeStrings(c.Media)
	c.Uncertainties = normalizeStrings(c.Uncertainties)
	return c
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, capsuleEntry := range values {
		capsuleEntry = strings.TrimSpace(capsuleEntry)
		if capsuleEntry == "" {
			continue
		}
		if _, ok := seen[capsuleEntry]; ok {
			continue
		}
		seen[capsuleEntry] = struct{}{}
		out = append(out, capsuleEntry)
	}
	return out
}

// Valid reports whether the capsule carries any semantic state.
func (c Capsule) Valid() bool {
	return c.Summary != "" || len(c.Goals)+len(c.Constraints)+len(c.Decisions)+len(c.Facts)+len(c.Artifacts)+len(c.ToolResults)+len(c.OpenLoops)+len(c.Media)+len(c.Uncertainties) > 0
}

func stripMarkdownJSON(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			trimmed = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	if start := strings.Index(trimmed, "{"); start > 0 {
		trimmed = trimmed[start:]
	}
	if end := strings.LastIndex(trimmed, "}"); end >= 0 && end+1 < len(trimmed) {
		trimmed = trimmed[:end+1]
	}
	return strings.TrimSpace(trimmed)
}

func roleOf(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	var role string
	_ = json.Unmarshal(fields["role"], &role)
	return strings.ToLower(strings.TrimSpace(role))
}

func typeOf(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	var itemType string
	_ = json.Unmarshal(fields["type"], &itemType)
	return strings.ToLower(strings.TrimSpace(itemType))
}

func protectedItem(raw json.RawMessage) bool {
	var jsonRoot any
	if json.Unmarshal(raw, &jsonRoot) != nil {
		return true
	}
	return protectedValue(jsonRoot)
}

func protectedValue(jsonNode any) bool {
	switch typed := jsonNode.(type) {
	case []any:
		for _, child := range typed {
			if protectedValue(child) {
				return true
			}
		}
	case map[string]any:
		return protectedObject(typed)
	}
	return false
}

func protectedObject(fields map[string]any) bool {
	for key, child := range fields {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if protectedKey(lowerKey) || (lowerKey == "type" && protectedType(child)) || protectedValue(child) {
			return true
		}
	}
	return false
}

func protectedKey(key string) bool {
	switch key {
	case "call_id", "tool_call_id", "tool_calls", "function_call", "functioncall", "functionresponse", "tool_use_id", "encrypted_content", "signature":
		return true
	default:
		return false
	}
}

func protectedType(jsonNode any) bool {
	typeName, ok := jsonNode.(string)
	if !ok {
		return false
	}
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	return strings.Contains(typeName, "tool") || strings.Contains(typeName, "function_call") || typeName == "reasoning" || strings.Contains(typeName, "compaction")
}

func extractText(raw json.RawMessage) string {
	var jsonRoot any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&jsonRoot) != nil {
		return ""
	}
	collector := textCollector{}
	collector.collect(jsonRoot, "")
	return strings.TrimSpace(strings.Join(collector.parts, "\n"))
}

type textCollector struct {
	parts []string
}

func (c *textCollector) collect(jsonNode any, key string) {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if mediaOrStructuralKey(lowerKey) {
		return
	}
	switch typed := jsonNode.(type) {
	case string:
		if lowerKey == "role" || lowerKey == "type" || lowerKey == "id" || lowerKey == "name" || lowerKey == "status" {
			return
		}
		if strings.TrimSpace(typed) != "" {
			c.parts = append(c.parts, typed)
		}
	case []any:
		for _, child := range typed {
			c.collect(child, key)
		}
	case map[string]any:
		for childKey, child := range typed {
			c.collect(child, childKey)
		}
	}
}

func mediaOrStructuralKey(key string) bool {
	switch key {
	case "image_url", "file_url", "video_url", "audio_url", "data", "base64", "inline_data", "inlinedata", "file_data", "filedata", "mime_type", "mimetype", "media_type":
		return true
	default:
		return false
	}
}

func scanMediaRefs(raw json.RawMessage) mediaScan {
	var jsonRoot any
	if json.Unmarshal(raw, &jsonRoot) != nil {
		return mediaScan{hasMedia: true, unsupported: true}
	}
	collector := mediaReferenceCollector{seen: map[string]struct{}{}}
	collector.collect(jsonRoot)
	return mediaScan{refs: collector.refs, hasMedia: collector.hasMedia, unsupported: collector.unsupported}
}

type mediaReferenceCollector struct {
	refs        []MediaRef
	seen        map[string]struct{}
	hasMedia    bool
	unsupported bool
}

func (c *mediaReferenceCollector) collect(jsonNode any) {
	switch typed := jsonNode.(type) {
	case []any:
		for _, child := range typed {
			c.collect(child)
		}
	case map[string]any:
		c.collectObject(typed)
	}
}

func (c *mediaReferenceCollector) collectObject(fields map[string]any) {
	if kind := mediaObjectKind(fields); kind != "" {
		c.collectObjectMedia(kind, fields)
		return
	}
	for key, child := range fields {
		c.collectField(key, child)
		if !mediaField(key) {
			c.collect(child)
		}
	}
}

func (c *mediaReferenceCollector) collectObjectMedia(kind string, fields map[string]any) {
	c.hasMedia = true
	url := mediaObjectURL(kind, fields)
	if kind != "image" || url == "" {
		c.unsupported = true
		return
	}
	c.append(kind, url)
}

func (c *mediaReferenceCollector) collectField(key string, jsonNode any) {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	if kind := mediaKindForKey(lowerKey); kind != "" {
		c.hasMedia = true
		url := mediaURL(jsonNode)
		if kind != "image" || url == "" {
			c.unsupported = true
			return
		}
		c.append(kind, url)
	}
	if lowerKey == "inline_data" || lowerKey == "inlinedata" {
		c.hasMedia = true
		url := inlineDataURL(jsonNode)
		if url == "" {
			c.unsupported = true
			return
		}
		c.append("image", url)
	}
}

func mediaObjectKind(fields map[string]any) string {
	itemType, _ := fields["type"].(string)
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "input_image", "image", "image_url":
		return "image"
	case "input_file", "file":
		return "file"
	case "input_video", "video":
		return "video"
	case "input_audio", "audio":
		return "audio"
	}
	return ""
}

func mediaObjectURL(kind string, fields map[string]any) string {
	if kind != "image" {
		return ""
	}
	for _, key := range []string{"image_url", "url"} {
		if url := mediaURL(fields[key]); url != "" {
			return url
		}
	}
	if source, ok := fields["source"].(map[string]any); ok {
		if url := mediaURL(source); url != "" {
			return url
		}
		return inlineDataURL(source)
	}
	return ""
}

func mediaField(key string) bool {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	return mediaKindForKey(lowerKey) != "" || lowerKey == "inline_data" || lowerKey == "inlinedata"
}

func mediaKindForKey(key string) string {
	switch key {
	case "image_url":
		return "image"
	case "file_url", "file_data", "filedata":
		return "file"
	case "video_url":
		return "video"
	case "audio_url":
		return "audio"
	default:
		return ""
	}
}

func (c *mediaReferenceCollector) append(kind, url string) {
	if url == "" {
		return
	}
	cacheKey := kind + "\x00" + url
	if _, ok := c.seen[cacheKey]; ok {
		return
	}
	c.seen[cacheKey] = struct{}{}
	c.refs = append(c.refs, MediaRef{Kind: kind, URL: url})
}

func mediaURL(jsonNode any) string {
	switch typed := jsonNode.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"url", "uri", "image_url", "file_url"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func inlineDataURL(jsonNode any) string {
	mediaObject, ok := jsonNode.(map[string]any)
	if !ok {
		return ""
	}
	encodedPayload, _ := mediaObject["data"].(string)
	mime, _ := mediaObject["mimeType"].(string)
	if mime == "" {
		mime, _ = mediaObject["mime_type"].(string)
	}
	if mime == "" {
		mime, _ = mediaObject["media_type"].(string)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/") || strings.TrimSpace(encodedPayload) == "" {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(encodedPayload); err != nil {
		return ""
	}
	return "data:" + strings.TrimSpace(mime) + ";base64," + encodedPayload
}

func containsNonImageMedia(refs []MediaRef) bool {
	for _, ref := range refs {
		if ref.Kind != "image" {
			return true
		}
	}
	return false
}
