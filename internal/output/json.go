package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/talentohq/talento-cli/internal/terminal"
)

func marshalTerminalJSON(value any, indent bool) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return escapeUnsafeJSON(buffer.Bytes()), nil
}

// escapeUnsafeJSON keeps structured data lossless while preventing raw terminal
// controls and invisible direction-changing characters from reaching a terminal.
func escapeUnsafeJSON(data []byte) []byte {
	var escaped []byte
	last := 0
	for index, current := range string(data) {
		if !terminal.UnsafeRune(current) && current != '\u200c' && current != '\u200d' {
			continue
		}
		if escaped == nil {
			escaped = make([]byte, 0, len(data)+6)
		}
		escaped = append(escaped, data[last:index]...)
		escaped = appendJSONRuneEscape(escaped, current)
		last = index + utf8.RuneLen(current)
	}
	if escaped == nil {
		return data
	}
	return append(escaped, data[last:]...)
}

func appendJSONRuneEscape(target []byte, current rune) []byte {
	if current <= 0xffff {
		return fmt.Appendf(target, `\u%04x`, current)
	}
	high, low := utf16.EncodeRune(current)
	return fmt.Appendf(target, `\u%04x\u%04x`, high, low)
}
