package agent

// CompressCooldown prevents rapid repeated compaction within a session.
// Built-in dead-loop detection: consecutive ineffective compactions (< 10%
// reduction) increase the cooldown period automatically.
type CompressCooldown struct {
	lastCompressIteration int
	cooldownIterations    int
	ineffectiveCount      int
	baseCooldown          int
}

// NewCompressCooldown creates a cooldown manager. Default cooldown is 3 iterations.
func NewCompressCooldown(iterations int) *CompressCooldown {
	if iterations <= 0 {
		iterations = 3
	}
	return &CompressCooldown{
		cooldownIterations: iterations,
		baseCooldown:       iterations,
	}
}

// ShouldTrigger returns true if enough iterations have passed since the last compaction.
func (c *CompressCooldown) ShouldTrigger(currentIteration int) bool {
	if c.lastCompressIteration == 0 {
		return true
	}
	return currentIteration-c.lastCompressIteration >= c.cooldownIterations
}

// RecordCompress records the iteration number when compaction occurred.
func (c *CompressCooldown) RecordCompress(iteration int) {
	c.lastCompressIteration = iteration
}

// RecordIneffective records an ineffective compaction (< 10% reduction).
// After 2 consecutive ineffective compactions, cooldown increases to 10 iterations.
func (c *CompressCooldown) RecordIneffective() {
	c.ineffectiveCount++
	if c.ineffectiveCount >= 2 {
		c.cooldownIterations = 10
	}
}

// RecordEffective records an effective compaction (>= 10% reduction), resetting
// the ineffective counter and cooldown period.
func (c *CompressCooldown) RecordEffective() {
	c.ineffectiveCount = 0
	if c.cooldownIterations > c.baseCooldown {
		c.cooldownIterations = c.baseCooldown
	}
}

// IneffectiveCount returns the current consecutive ineffective compaction count.
func (c *CompressCooldown) IneffectiveCount() int {
	return c.ineffectiveCount
}

// Reset clears cooldown state.
func (c *CompressCooldown) Reset() {
	c.lastCompressIteration = 0
}

// shouldCompact returns true when the token count exceeds the compaction
// threshold (75% of max). This replaces the previous 3-factor dynamic
// threshold with a simple headroom check.
func shouldCompact(totalTokens, maxTokens int) bool {
	if maxTokens <= 0 {
		return false
	}
	return float64(totalTokens) >= float64(maxTokens)*0.75
}
