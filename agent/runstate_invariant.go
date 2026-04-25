package agent

import (
	"context"
	"fmt"

	log "xbot/logger"
)

// ValidateInvariants checks internal consistency of the runState.
// It is intended for debug-mode validation at key transition points
// (after LLM calls, after compression, after persistence).
// Returns nil if all invariants hold, or an error describing the first violation.
func (s *runState) ValidateInvariants() error {
	// Invariant 1: persistence watermark must not exceed message count
	if pc := s.persistence.LastPersistedCount(); pc > len(s.messages) {
		return fmt.Errorf("invariant violation: LastPersistedCount(%d) > len(messages)(%d)", pc, len(s.messages))
	}

	// Invariant 2: promptTokens > 0 iff hadLLMCall || restoredFromDB
	hasTokens := s.tokenTracker.PromptTokens() > 0
	hasSource := s.tokenTracker.HadLLMCall() || s.tokenTracker.RestoredFromDB()
	if hasTokens != hasSource {
		return fmt.Errorf("invariant violation: promptTokens=%d but hadLLMCall=%v restoredFromDB=%v",
			s.tokenTracker.PromptTokens(), s.tokenTracker.HadLLMCall(), s.tokenTracker.RestoredFromDB())
	}

	// Invariant 3: msgCountAtCall <= len(messages), unless after compress
	// After compress, msgCountAtCall is reset to the new (smaller) message count,
	// so it should always be <= len(messages).
	if mc := s.tokenTracker.MsgCountAtCall(); mc > len(s.messages) {
		return fmt.Errorf("invariant violation: msgCountAtCall(%d) > len(messages)(%d)",
			mc, len(s.messages))
	}

	return nil
}

// validateInvariantsAt logs invariant violations at debug level.
// Intended as a non-intrusive check at key transition points.
func (s *runState) validateInvariantsAt(ctx context.Context, point string) {
	if err := s.ValidateInvariants(); err != nil {
		log.Ctx(ctx).WithField("invariant_check", point).WithError(err).Debug("runState invariant violation")
	}
}
