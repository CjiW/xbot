package agent

import (
	"crypto/sha256"
	"encoding/binary"
)

// deriveSubAgentTenantID generates a stable, deterministic tenantID for a SubAgent.
//
// The SubAgent's chat session is modeled as a private chat with its caller Agent:
// the caller is the "user" and the SubAgent is "xbot". This maintains a consistent
// agent-logic abstraction and naturally isolates SubAgent memory from the parent.
//
// The tenantID is derived from (parentTenantID, parentAgentID, roleName) using
// SHA-256 to ensure uniqueness and determinism. The same combination always
// produces the same tenantID, so a SubAgent's memory persists across calls.
//
// The negative sign bit is set (MSB of int64) to distinguish SubAgent tenantIDs
// from normal IM-derived tenantIDs (which are always positive from SQLite auto-increment).
func deriveSubAgentTenantID(parentTenantID int64, parentAgentID, roleName string) int64 {
	h := sha256.New()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(parentTenantID))
	h.Write(buf)
	h.Write([]byte(parentAgentID))
	h.Write([]byte{0}) // separator
	h.Write([]byte(roleName))
	sum := h.Sum(nil)

	// Take first 8 bytes and ensure negative (set sign bit)
	raw := binary.BigEndian.Uint64(sum[:8])
	raw |= uint64(1) << 63 // guarantee negative via sign bit
	return int64(raw)
}
