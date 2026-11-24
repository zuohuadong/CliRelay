package executor

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex can declare tool search so that a large MCP catalog is loaded on
// demand: a {"type":"tool_search","execution":"client"} tool, the matching
// tool_search_call/tool_search_output history items, and later declarations in
// Responses Lite additional_tools items. xAI runs tool search itself and
// refuses a client-executed one:
//
//	400 tool_search execution must be 'server' or not set;
//	    client-executed tool search is not supported
//
// A declaration left without its execution field means xAI's own server-side
// search, which is a different tool than the one Codex dispatches, so both
// shapes are dropped. The client keeps its catalog and simply loads tools
// eagerly on the next turn.
const (
	// xaiToolSearchToolType is the Responses declaration Codex sends.
	xaiToolSearchToolType = "tool_search"
	// xaiToolSearchCallType and xaiToolSearchOutputType are the history items a
	// finished client search leaves behind; xAI has no such input items.
	xaiToolSearchCallType   = "tool_search_call"
	xaiToolSearchOutputType = "tool_search_output"
	// xaiAdditionalToolsItemType is the Responses Lite history item that can
	// carry further tool declarations, tool search included.
	xaiAdditionalToolsItemType = "additional_tools"
)

// stripXAIUnsupportedToolSearch removes every client tool_search declaration
// and the history items that belong to it. Requests that carry none are
// returned byte for byte.
func stripXAIUnsupportedToolSearch(body []byte) []byte {
	if !bytes.Contains(body, []byte(xaiToolSearchToolType)) {
		return body
	}

	removed := 0
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		if raw, dropped := xaiWithoutToolSearchTools(tools); dropped > 0 {
			if updated, err := sjson.SetRawBytes(body, "tools", raw); err == nil {
				body = updated
				removed += dropped
			}
		}
	}
	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		if raw, dropped := xaiWithoutToolSearchInput(input); dropped > 0 {
			if updated, err := sjson.SetRawBytes(body, "input", raw); err == nil {
				body = updated
				removed += dropped
			}
		}
	}
	if choice := gjson.GetBytes(body, "tool_choice"); choice.IsObject() {
		body = xaiWithoutToolSearchChoice(body, choice)
	}
	if removed > 0 {
		log.Debugf("xai: dropped %d tool_search declaration(s); this endpoint only runs tool search on its own", removed)
	}
	return body
}

// xaiWithoutToolSearchTools rebuilds a tools array without tool_search
// declarations, including the ones nested in a namespace tool, and reports how
// many it removed.
func xaiWithoutToolSearchTools(tools gjson.Result) ([]byte, int) {
	kept := make([][]byte, 0, len(tools.Array()))
	removed := 0
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case xaiToolSearchToolType:
			removed++
			continue
		case xaiNamespaceToolType:
			if raw, dropped := xaiWithoutToolSearchTools(tool.Get("tools")); dropped > 0 {
				if updated, err := sjson.SetRawBytes([]byte(tool.Raw), "tools", raw); err == nil {
					kept = append(kept, updated)
					removed += dropped
					continue
				}
			}
		}
		kept = append(kept, []byte(tool.Raw))
	}
	if removed == 0 {
		return nil, 0
	}
	return xaiJoinRawJSONArray(kept), removed
}

// xaiWithoutToolSearchInput drops the tool_search history items and, from the
// Lite additional_tools items, the declarations that belong to them.
func xaiWithoutToolSearchInput(items gjson.Result) ([]byte, int) {
	kept := make([][]byte, 0, len(items.Array()))
	removed := 0
	for _, item := range items.Array() {
		switch item.Get("type").String() {
		case xaiToolSearchCallType, xaiToolSearchOutputType:
			removed++
			continue
		case xaiAdditionalToolsItemType:
			if raw, dropped := xaiWithoutToolSearchTools(item.Get("tools")); dropped > 0 {
				if updated, err := sjson.SetRawBytes([]byte(item.Raw), "tools", raw); err == nil {
					kept = append(kept, updated)
					removed += dropped
					continue
				}
			}
		}
		kept = append(kept, []byte(item.Raw))
	}
	if removed == 0 {
		return nil, 0
	}
	return xaiJoinRawJSONArray(kept), removed
}

// xaiWithoutToolSearchChoice keeps a forced or allowed choice from naming a
// declaration that just went away.
func xaiWithoutToolSearchChoice(body []byte, choice gjson.Result) []byte {
	switch choice.Get("type").String() {
	case xaiToolSearchToolType:
		if updated, err := sjson.DeleteBytes(body, "tool_choice"); err == nil {
			return updated
		}
		return body
	case "allowed_tools":
		entries := choice.Get("tools")
		if !entries.IsArray() {
			return body
		}
		kept := make([][]byte, 0, len(entries.Array()))
		dropped := false
		for _, entry := range entries.Array() {
			if entry.Get("type").String() == xaiToolSearchToolType {
				dropped = true
				continue
			}
			kept = append(kept, []byte(entry.Raw))
		}
		if !dropped {
			return body
		}
		if len(kept) == 0 {
			if updated, err := sjson.DeleteBytes(body, "tool_choice"); err == nil {
				return updated
			}
			return body
		}
		if updated, err := sjson.SetRawBytes(body, "tool_choice.tools", xaiJoinRawJSONArray(kept)); err == nil {
			return updated
		}
	}
	return body
}

// normalizeXAIAllowedToolsChoice removes a tool_choice shape xAI has no
// variant for. Clients that restrict a turn to a set of tools send
//
//	{"type":"allowed_tools","tools":[...]}
//
// and xAI answers 422 "tool_choice: data did not match any variant of untagged
// enum ModelToolChoice" — with or without a mode field. Dropping it leaves the
// endpoint on its default auto choice, which is the closest thing it has.
func normalizeXAIAllowedToolsChoice(body []byte) []byte {
	choice := gjson.GetBytes(body, "tool_choice")
	if !choice.IsObject() || choice.Get("type").String() != "allowed_tools" {
		return body
	}
	updated, err := sjson.DeleteBytes(body, "tool_choice")
	if err != nil {
		return body
	}
	log.Debugf("xai: dropped an allowed_tools choice; this endpoint only knows auto, none and a forced tool")
	return updated
}
