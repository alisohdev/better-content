package textnorm

import "testing"

func TestTrimECMAScriptWhitespaceSet(t *testing.T) {
	if got := TrimECMAScript("\uFEFF \u2003value\u3000\uFEFF"); got != "value" {
		t.Fatalf("TrimECMAScript = %q", got)
	}
	if got := TrimECMAScript("\u0085value\u0085"); got != "\u0085value\u0085" {
		t.Fatalf("TrimECMAScript trimmed U+0085: %q", got)
	}
}
