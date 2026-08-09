package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PurgeStoredBodies removes durable payload sections from existing request log
// files while retaining request identity, headers, response status, and error
// status metadata.
func (l *FileRequestLogger) PurgeStoredBodies() error {
	if l == nil || strings.TrimSpace(l.logsDir) == "" {
		return nil
	}
	entries, errReadDir := os.ReadDir(l.logsDir)
	if errReadDir != nil {
		if os.IsNotExist(errReadDir) {
			return nil
		}
		return errReadDir
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		path := filepath.Join(l.logsDir, entry.Name())
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			return errRead
		}
		sanitized := stripRequestLogBodies(string(raw))
		if sanitized == string(raw) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			return errInfo
		}
		tempFile, errCreate := os.CreateTemp(l.logsDir, ".request-log-purge-*")
		if errCreate != nil {
			return errCreate
		}
		tempName := tempFile.Name()
		errWrite := error(nil)
		if _, errWrite = io.WriteString(tempFile, sanitized); errWrite == nil {
			errWrite = tempFile.Chmod(info.Mode().Perm())
		}
		if errClose := tempFile.Close(); errWrite == nil {
			errWrite = errClose
		}
		if errWrite != nil {
			_ = os.Remove(tempName)
			return errWrite
		}
		if errRename := os.Rename(tempName, path); errRename != nil {
			_ = os.Remove(tempName)
			return errRename
		}
	}
	return nil
}

func stripRequestLogBodies(raw string) string {
	lines := strings.Split(raw, "\n")
	var output strings.Builder
	section := ""
	skipBody := false
	for _, line := range lines {
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "=== "), " ===")
			skipBody = false
			output.WriteString(line)
			output.WriteByte('\n')
			switch section {
			case "REQUEST BODY", "WEBSOCKET TIMELINE", "API WEBSOCKET TIMELINE", "API REQUEST", "API RESPONSE":
				output.WriteString("<not stored>\n")
				skipBody = true
			case "API ERROR RESPONSE":
				skipBody = true
			}
			continue
		}
		if skipBody {
			if section == "API ERROR RESPONSE" && strings.HasPrefix(line, "HTTP Status:") {
				output.WriteString(line)
				output.WriteByte('\n')
			}
			continue
		}
		if section == "RESPONSE" && line == "" {
			output.WriteByte('\n')
			output.WriteString("<not stored>\n")
			skipBody = true
			continue
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return output.String()
}
