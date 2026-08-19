package events

// Redact returns payload with any sensitive fields removed or masked. No
// field in L01's event set is sensitive yet, so this is a pass-through;
// L11 (policy + secrets) gives it real logic once effects can carry
// credentials or PII into an event payload.
func Redact(payload []byte) []byte {
	return payload
}
