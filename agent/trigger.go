package agent

// shouldCompact returns true when the token count exceeds the compaction
// threshold (75% of max). This replaces the previous 3-factor dynamic
// threshold with a simple headroom check.
func shouldCompact(totalTokens, maxTokens int) bool {
	if maxTokens <= 0 {
		return false
	}
	return float64(totalTokens) >= float64(maxTokens)*0.75
}
