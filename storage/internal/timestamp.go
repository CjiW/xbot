package internal

import "time"

// ParseTimestamp parses a timestamp string from SQLite.
// New rows are stored in RFC3339 format (since the format was unified).
// Legacy rows in local wall-clock format "2006-01-02 15:04:05" are supported
// for backward compatibility. Legacy "...Z" timestamps from older SQLite
// driver versions are treated as local wall-clock values to avoid timezone shifts.
func ParseTimestamp(s string) time.Time {
	// Try RFC3339 first (new format)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local()
	}
	// Legacy local wall-clock format
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t
	}
	// Legacy "...Z" format treated as local wall-clock
	if t, err := time.ParseInLocation("2006-01-02T15:04:05Z", s, time.Local); err == nil {
		return t
	}

	return time.Time{}
}
