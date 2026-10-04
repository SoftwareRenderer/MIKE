package types

// IsWordByte reports the bytes that continue a word. The search endpoint's whole-word filter and the
// edit tool's symbol match ask the same question of the same line, and must answer it the same way:
// where one sees a word boundary and the other does not, a replacement is offered for a match the
// search never listed.
func IsWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}
