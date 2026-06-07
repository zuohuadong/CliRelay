package vision

import (
	"strings"
)

// HasImageContent checks if a message content array contains image inputs
func HasImageContent(content []map[string]any) bool {
	for _, item := range content {
		if itemType, ok := item["type"].(string); ok {
			if itemType == "image_url" || itemType == "input_image" {
				return true
			}
		}
	}
	return false
}

// ExtractImageURLs extracts image URLs/data URLs from message content
func ExtractImageURLs(content []map[string]any) []string {
	var urls []string
	for _, item := range content {
		if itemType, ok := item["type"].(string); ok {
			if itemType == "image_url" {
				if imgURL, ok := item["image_url"].(map[string]any); ok {
					if url, ok := imgURL["url"].(string); ok && url != "" {
						urls = append(urls, url)
					}
				}
				if url, ok := item["url"].(string); ok && url != "" {
					urls = append(urls, url)
				}
			}
			if itemType == "input_image" {
				if url, ok := item["image_url"].(string); ok && url != "" {
					urls = append(urls, url)
				}
			}
		}
	}
	return urls
}

// IsDataURL checks if a string is a data URL (base64-encoded)
func IsDataURL(s string) bool {
	return strings.HasPrefix(s, "data:")
}
