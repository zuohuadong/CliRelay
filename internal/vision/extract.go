package vision

// ExtractImageData extracts base64 data and MIME type from a parsed ImagePart.
// Returns the raw base64 string and a normalized MIME type.
func ExtractImageData(ip *ImagePart) (data string, mimeType string) {
	if ip.Data != "" {
		mime := ip.MIMEType
		if mime == "" {
			mime = "image/png"
		}
		return ip.Data, mime
	}
	return "", ""
}

// IsRemoteOnly returns true when the image is only available via remote URL
// (not inline base64 in the current request).
func IsRemoteOnly(ip *ImagePart) bool {
	return ip.Data == "" && ip.RemoteURL != ""
}
