package agent

// shouldCompact returns true when the token count exceeds the compaction
// threshold (default 90% of max, configurable via CompressionThreshold).
// This replaces the previous 3-factor dynamic threshold with a simple headroom check.
func shouldCompact(totalTokens, maxTokens int, threshold float64) bool {
	if maxTokens <= 0 {
		return false
	}
	if threshold <= 0 {
		threshold = 0.9
	}
	return float64(totalTokens) >= float64(maxTokens)*threshold
}
