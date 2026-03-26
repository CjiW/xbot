package internal

import (
	"strings"
	"time"
)

// ParseTimestamp parses a timestamp string from SQLite.
//
// The modernc.org/sqlite driver reformats DATETIME values on read, appending a "Z"
// suffix to bare local wall-clock timestamps (e.g. "2006-01-02 15:04:05" →
// "2006-01-02T15:04:05Z"). This "Z" does NOT mean UTC — it's a local wall-clock
// value that the driver reformatted. We detect this case and parse in local location.
//
// Production code stores timestamps in RFC3339 format with explicit offset
// (e.g. "2026-03-05T14:30:00+08:00"), which is parsed correctly by time.Parse.
//
// Supported formats (in priority order):
//  1. RFC3339 with timezone offset (e.g. "2026-03-05T14:30:00+08:00")
//  2. "Z"-suffixed strings treated as local wall-clock (SQLite driver reformatting)
//  3. Legacy bare local format "2006-01-02 15:04:05"
func ParseTimestamp(s string) time.Time {
	// RFC3339 with explicit timezone offset (e.g. "2026-03-05T14:30:00+08:00")
	// These come from production code using time.Now().Format(time.RFC3339).
	if !strings.HasSuffix(s, "Z") {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Local()
		}
	}

	// "Z"-suffixed strings: treated as local wall-clock, not UTC.
	// The SQLite driver appends "Z" when reformatting DATETIME columns, even for
	// local wall-clock values. Parsing with time.Parse would interpret as UTC and
	// .Local() would shift by the timezone offset, producing wrong times.
	if t, err := time.ParseInLocation("2006-01-02T15:04:05Z", s, time.Local); err == nil {
		return t
	}

	// Legacy bare local format (no longer produced but may exist in old rows)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t
	}

	return time.Time{}
}
