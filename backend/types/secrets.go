package types

// MaskedSecret stands in for a stored secret in a response, and reads as "unchanged" on the way back.
// It is one contract with MASKED_SECRET in frontend/src/lib/api/helpers.ts, pinned by secrets_test.go.
const MaskedSecret = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets
