package types

import "testing"

// If the two literals stop matching, a mask gets stored in place of the credential it stood for.
func TestMaskedSecretIsWhatTheBrowserSendsBack(t *testing.T) {
	const browserLiteral = "••••••••••"
	if MaskedSecret != browserLiteral {
		t.Errorf("MaskedSecret is %q but the browser sends %q — change both or a mask overwrites a secret",
			MaskedSecret, browserLiteral)
	}
	if n := len([]rune(MaskedSecret)); n != 10 {
		t.Errorf("MaskedSecret is %d runes, want the 10 the form renders", n)
	}
}
