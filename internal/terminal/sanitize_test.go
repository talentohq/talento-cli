package terminal

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTerminalContent(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain Unicode", value: "Café ☕", want: "Café ☕"},
		{name: "CSI", value: "before\x1b[31mred\x1b[0mafter", want: "beforeredafter"},
		{name: "OSC BEL", value: "before\x1b]0;forged title\x07after", want: "beforeafter"},
		{name: "OSC ST", value: "before\x1b]52;c;secret\x1b\\after", want: "beforeafter"},
		{name: "OSC payload UTF-8 cannot mimic ST", value: "before\x1b]0;hiddenÜFORGED\x07after", want: "beforeafter"},
		{name: "C1 CSI", value: "before\u009b31mredafter", want: "beforeredafter"},
		{name: "C1 OSC", value: "before\u009dtitle\u009cafter", want: "beforeafter"},
		{name: "C1 OSC payload UTF-8 cannot mimic ST", value: "before\u009dhiddenÜFORGED\u009cafter", want: "beforeafter"},
		{name: "controls", value: "a\x00b\rc\bd\ve\x7f", want: "abcde"},
		{name: "bidi", value: "invoice\u202efdp.exe", want: "invoicefdp.exe"},
		{name: "invisible", value: "pay\u200bpal\u2060.example", want: "paypal.example"},
		{name: "isolated tag", value: "pay\U000e0061pal", want: "paypal"},
		{name: "multiline", value: "line one\nline\ttwo", want: "line one\nline\ttwo"},
		{name: "right to left language", value: "مرحبا שלום", want: "مرحبا שלום"},
		{name: "emoji joiners", value: "👨\u200d👩\u200d👧", want: "👨\u200d👩\u200d👧"},
		{name: "emoji subdivision flag tags", value: "\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f", want: "\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f"},
		{name: "Persian non-joiner", value: "می\u200cخواهم", want: "می\u200cخواهم"},
		{name: "ASCII joiner spoof", value: "pay\u200dpal", want: "paypal"},
		{name: "decomposed accent", value: "Cafe\u0301", want: "Cafe\u0301"},
		{name: "invalid UTF-8", value: "caf\xe9.txt", want: "caf.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Sanitize(test.value); got != test.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestSanitizeLineContainsLineBreaks(t *testing.T) {
	if got := SanitizeLine("first\nsecond\t\x1b[2Jthird"); got != "first second third" {
		t.Fatalf("SanitizeLine() = %q", got)
	}
}

func FuzzSanitizeIsIdempotentAndContainsUnsafeRunes(f *testing.F) {
	for _, seed := range []string{"ordinary", "\x1b[31mred", "\x1b]52;c;secret\x07", "a\u009bb", "invoice\u202efdp.exe", "👨\u200d👩"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		sanitized := Sanitize(value)
		if Sanitize(sanitized) != sanitized {
			t.Fatalf("Sanitize is not idempotent for %q", value)
		}
		if !utf8.ValidString(sanitized) {
			t.Fatalf("Sanitize returned invalid UTF-8 for %q", value)
		}
		for _, current := range sanitized {
			if current != '\n' && current != '\t' && UnsafeRune(current) {
				if !strings.ContainsRune("\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f", current) {
					t.Fatalf("Sanitize(%q) retained unsafe rune U+%04X", value, current)
				}
			}
		}
		if strings.ContainsRune(sanitized, '\x1b') {
			t.Fatalf("Sanitize(%q) retained ESC", value)
		}
	})
}
