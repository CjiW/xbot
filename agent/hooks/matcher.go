package hooks

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Matcher determines whether a given Event matches a configured pattern.
// It supports four matching modes: match-all, exact, multi-select, and regex.
// An optional if-condition provides finer filtering based on tool input values.
type Matcher struct {
	pattern  string         // original pattern string
	regex    *regexp.Regexp // pre-compiled regex (may be nil)
	exact    []string       // exact match list (multi-select has multiple entries)
	matchAll bool           // true when pattern is "" or "*"
	ifCond   string         // raw if-condition string
	ifRegex  *regexp.Regexp // pre-compiled argPattern from if-condition (may be nil)
	ifTool   string         // parsed tool name from if-condition
}

// isExactPattern returns true if the pattern consists only of word characters
// (\w) and pipe separators (|), with no consecutive pipes.
func isExactPattern(s string) bool {
	if s == "" {
		return false
	}
	// Must only contain \w and |
	for _, ch := range s {
		if !isWordChar(ch) && ch != '|' {
			return false
		}
	}
	// Must not contain consecutive ||
	if strings.Contains(s, "||") {
		return false
	}
	// Must not start or end with |
	if s[0] == '|' || s[len(s)-1] == '|' {
		return false
	}
	return true
}

func isWordChar(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_'
}

// NewMatcher creates a new Matcher from the given pattern string.
//
// Matching mode is determined by the pattern:
//   - "" or "*" → match all events (matchAll=true)
//   - Only word chars and pipe separators (e.g. "Shell", "Shell|FileCreate") → exact match
//   - Everything else → treated as a regular expression, pre-compiled at creation time
func NewMatcher(pattern string) *Matcher {
	m := &Matcher{pattern: pattern}

	trimmed := strings.TrimSpace(pattern)

	// Match-all: empty or "*"
	if trimmed == "" || trimmed == "*" {
		m.matchAll = true
		return m
	}

	// Exact / multi-select pattern
	if isExactPattern(trimmed) {
		m.exact = strings.Split(trimmed, "|")
		return m
	}

	// Regex pattern — pre-compile
	m.regex = regexp.MustCompile(trimmed)
	return m
}

// Match checks whether the event's tool name matches the pattern.
// This is the coarse filter; use MatchIf afterwards for the fine-grained
// if-condition check.
func (m *Matcher) Match(event Event) bool {
	// 1. Match-all
	if m.matchAll {
		return true
	}

	toolName := event.ToolName()

	// 2. Exact match
	if len(m.exact) > 0 {
		for _, e := range m.exact {
			if toolName == e {
				return true
			}
		}
		return false
	}

	// 3. Regex match
	if m.regex != nil {
		return m.regex.MatchString(toolName)
	}

	// 4. No pattern matched
	return false
}

// SetIf sets the if-condition for fine-grained filtering and returns the
// Matcher for chaining. The if-condition format is: ToolName(argPattern)
// where argPattern may contain * wildcards (treated as .* regex).
func (m *Matcher) SetIf(ifCond string) *Matcher {
	m.ifCond = ifCond
	m.parseIfCondition()
	return m
}

// parseIfCondition parses the if-condition string into tool name and
// argument pattern components.
func (m *Matcher) parseIfCondition() {
	if m.ifCond == "" {
		return
	}

	s := strings.TrimSpace(m.ifCond)

	// Find the opening paren
	openIdx := strings.Index(s, "(")
	if openIdx < 0 {
		// No parens — treat entire string as tool name match
		m.ifTool = s
		return
	}

	closeIdx := strings.LastIndex(s, ")")
	if closeIdx < openIdx {
		closeIdx = len(s)
	}

	m.ifTool = s[:openIdx]
	argPattern := s[openIdx+1 : closeIdx]

	if argPattern != "" {
		// Convert glob-style wildcard * to regex .*
		escaped := regexp.QuoteMeta(argPattern)
		regexStr := strings.ReplaceAll(escaped, `\*`, ".*")
		m.ifRegex = regexp.MustCompile("^" + regexStr + "$")
	}
}

// MatchIf checks whether the event passes the if-condition filter.
// Returns true when there is no if-condition (no additional filtering).
// Should be called after Match() for fine-grained filtering.
func (m *Matcher) MatchIf(event Event) bool {
	if m.ifCond == "" {
		return true
	}

	// Check tool name match
	toolName := event.ToolName()
	if toolName != m.ifTool {
		return false
	}

	// If no arg pattern, tool name match is sufficient
	if m.ifRegex == nil {
		return true
	}

	// Check if any string value in ToolInput matches the arg pattern
	input := event.ToolInput()
	if input == nil {
		return false
	}

	return m.hasMatchingValue(input)
}

// hasMatchingValue recursively walks the input map and checks if any string
// value matches the if-condition argument pattern.
func (m *Matcher) hasMatchingValue(input map[string]any) bool {
	for _, v := range input {
		switch val := v.(type) {
		case string:
			if m.ifRegex.MatchString(val) {
				return true
			}
		case map[string]any:
			if m.hasMatchingValue(val) {
				return true
			}
		case []any:
			for _, item := range val {
				switch iv := item.(type) {
				case string:
					if m.ifRegex.MatchString(iv) {
						return true
					}
				case map[string]any:
					if m.hasMatchingValue(iv) {
						return true
					}
				}
			}
		case json.Number:
			if m.ifRegex.MatchString(val.String()) {
				return true
			}
		}
	}
	return false
}
