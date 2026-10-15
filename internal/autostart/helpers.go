package autostart

import "strings"

// firstLine returns the first line of command output, trimmed. Command output
// can be multi-line (systemd and Windows tools both are), and only the first
// line is useful in an error message or a Detail string.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
