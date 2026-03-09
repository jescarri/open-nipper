package session

import (
	"fmt"
	"strings"
	"unicode"
)

// BuildSessionKey constructs the canonical session key.
func BuildSessionKey(userID, channelType, sessionID string) string {
	return fmt.Sprintf("user:%s:channel:%s:session:%s", userID, channelType, sessionID)
}

// ParseSessionKey decodes a session key into its component parts.
// Format: user:{userID}:channel:{channelType}:session:{sessionID}
func ParseSessionKey(key string) (userID, channelType, sessionID string, err error) {
	const userPrefix = "user:"
	const chanMarker = ":channel:"
	const sessMarker = ":session:"

	if !strings.HasPrefix(key, userPrefix) {
		return "", "", "", fmt.Errorf("invalid session key (missing 'user:' prefix): %q", key)
	}
	rest := key[len(userPrefix):]

	chanIdx := strings.Index(rest, chanMarker)
	if chanIdx < 0 {
		return "", "", "", fmt.Errorf("invalid session key (missing ':channel:'): %q", key)
	}
	userID = rest[:chanIdx]
	rest = rest[chanIdx+len(chanMarker):]

	sessIdx := strings.Index(rest, sessMarker)
	if sessIdx < 0 {
		return "", "", "", fmt.Errorf("invalid session key (missing ':session:'): %q", key)
	}
	channelType = rest[:sessIdx]
	sessionID = rest[sessIdx+len(sessMarker):]

	if userID == "" || channelType == "" || sessionID == "" {
		return "", "", "", fmt.Errorf("invalid session key (empty component): %q", key)
	}
	return userID, channelType, sessionID, nil
}

// SanitizeSessionID converts s into a string that is safe to use as a
// filesystem path component:
//   - Alphanumeric characters, hyphens, and underscores are kept as-is.
//   - All other characters (dots, @, /, spaces, etc.) are replaced with "-".
//   - Leading and trailing hyphens are trimmed.
//   - Empty strings are returned as "default".
func SanitizeSessionID(s string) string {
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			// skip
		} else {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "default"
	}
	return result
}
