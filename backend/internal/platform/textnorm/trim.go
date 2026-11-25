package textnorm

import "strings"

// TrimECMAScript matches String.prototype.trim's WhiteSpace and
// LineTerminator set, including BOM and excluding non-breaking control U+0085.
func TrimECMAScript(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return r == '\u0009' || r == '\u000A' || r == '\u000B' || r == '\u000C' || r == '\u000D' ||
			r == '\u0020' || r == '\u00A0' || r == '\u1680' || (r >= '\u2000' && r <= '\u200A') ||
			r == '\u2028' || r == '\u2029' || r == '\u202F' || r == '\u205F' || r == '\u3000' || r == '\uFEFF'
	})
}
