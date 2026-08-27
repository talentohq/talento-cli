// Package terminal contains the final containment boundary for text written to
// an interactive terminal. It deliberately does not mutate data retained for
// structured output.
package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxCombiningMarks  = 8
	zeroWidthNonJoiner = '\u200c'
	zeroWidthJoiner    = '\u200d'
)

// Sanitize removes terminal escape sequences, controls, bidirectional controls,
// and invisible spoofing characters. Newlines and tabs remain available to
// multi-line human and Markdown renderers.
func Sanitize(value string) string {
	value = stripEscapeSequences(value)
	validTags := validEmojiTagOffsets(value)
	p := sanitizePass{input: value, lastBase: -1}
	for index, current := range value {
		switch {
		case current == '\n' || current == '\t':
			p.keep(current)
			p.marks = 0
		case current == utf8.RuneError && !strings.HasPrefix(value[index:], "\ufffd"):
			p.drop(index)
		case validTags[index]:
			p.keep(current)
		case UnsafeRune(current):
			p.drop(index)
		case current == zeroWidthJoiner || current == zeroWidthNonJoiner:
			p.holdJoiner(index, current)
		case isCombiningMark(current):
			if p.marks < maxCombiningMarks {
				p.keep(current)
				p.marks++
			} else {
				p.drop(index)
			}
		default:
			p.keep(current)
			if !unicode.Is(unicode.Cf, current) {
				p.marks = 0
			}
		}
	}
	return p.result()
}

// SanitizeLine contains text for a single-line terminal location.
func SanitizeLine(value string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(value))
}

// UnsafeRune reports code points that must not be emitted raw to a terminal.
// Structured serializers may escape these runes instead of removing them.
func UnsafeRune(current rune) bool {
	if current == '\n' || current == '\t' {
		return false
	}
	return isControl(current) || isBidiControl(current) || isInvisible(current)
}

type sanitizePass struct {
	input       string
	output      *strings.Builder
	lastBase    rune
	marks       int
	joiner      rune
	joinerIndex int
}

func (p *sanitizePass) keep(current rune) {
	if p.joiner != 0 {
		if isBase(current) {
			p.write(p.joiner)
		} else {
			p.diverge(p.joinerIndex)
		}
		p.joiner = 0
	}
	p.write(current)
	if !isCombiningMark(current) {
		p.lastBase = current
	}
}

func (p *sanitizePass) holdJoiner(index int, current rune) {
	if p.joiner != 0 || !isBase(p.lastBase) {
		p.drop(index)
		return
	}
	p.joiner = current
	p.joinerIndex = index
}

func (p *sanitizePass) drop(index int) {
	if p.joiner != 0 {
		index = p.joinerIndex
		p.joiner = 0
	}
	p.diverge(index)
}

func (p *sanitizePass) write(current rune) {
	if p.output != nil {
		p.output.WriteRune(current)
	}
}

func (p *sanitizePass) diverge(index int) {
	if p.output != nil {
		return
	}
	p.output = &strings.Builder{}
	p.output.Grow(len(p.input))
	p.output.WriteString(p.input[:index])
}

func (p *sanitizePass) result() string {
	if p.joiner != 0 {
		p.diverge(p.joinerIndex)
	}
	if p.output == nil {
		return p.input
	}
	return p.output.String()
}

func isBase(current rune) bool {
	return current >= utf8.RuneSelf && !unicode.IsSpace(current) &&
		!isCombiningMark(current) && !unicode.Is(unicode.Cf, current) && !unicode.IsPunct(current)
}

func isCombiningMark(current rune) bool {
	return current >= 0x300 && unicode.In(current, unicode.Mn, unicode.Me, unicode.Mc)
}

func isControl(current rune) bool {
	return current < 0x20 || current == 0x7f || (current >= 0x80 && current <= 0x9f)
}

func isBidiControl(current rune) bool {
	switch {
	case current == 0x061c, current == 0x200e, current == 0x200f:
		return true
	case current >= 0x202a && current <= 0x202e:
		return true
	case current >= 0x2066 && current <= 0x2069:
		return true
	default:
		return false
	}
}

func isInvisible(current rune) bool {
	switch {
	case current == 0x00ad, current == 0x034f, current == 0x180e, current == 0x200b, current == 0xfeff:
		return true
	case current >= 0x2060 && current <= 0x2064:
		return true
	case current >= 0x206a && current <= 0x206f:
		return true
	case current >= 0xe0000 && current <= 0xe007f:
		return true
	default:
		return false
	}
}

// validEmojiTagOffsets identifies the invisible tag characters that carry a
// well-formed emoji tag sequence. Isolated tags remain spoofing characters and
// are removed, while subdivision flags keep their meaningful tag payload.
func validEmojiTagOffsets(value string) map[int]bool {
	var valid map[int]bool
	for index, current := range value {
		if current != '\U0001f3f4' {
			continue
		}
		cursor := index + utf8.RuneLen(current)
		var sequence []int
		for cursor < len(value) {
			tag, size := utf8.DecodeRuneInString(value[cursor:])
			switch {
			case tag >= 0xe0020 && tag <= 0xe007e:
				sequence = append(sequence, cursor)
				cursor += size
			case tag == 0xe007f && len(sequence) > 0:
				if valid == nil {
					valid = make(map[int]bool, len(sequence)+1)
				}
				for _, offset := range sequence {
					valid[offset] = true
				}
				valid[cursor] = true
				cursor = len(value)
			default:
				cursor = len(value)
			}
		}
	}
	return valid
}

// stripEscapeSequences removes both seven-bit ESC sequences and their eight-bit
// C1 forms. String sequences are consumed through BEL or ST so their payload does
// not remain as attacker-selected visible debris.
func stripEscapeSequences(value string) string {
	var output strings.Builder
	output.Grow(len(value))
	for index := 0; index < len(value); {
		switch {
		case value[index] == 0x1b:
			index = consumeEscape(value, index)
		case value[index] >= 0x80 && value[index] <= 0x9f:
			index = consumeC1(value, index, value[index], 1)
		case value[index] == 0xc2 && index+1 < len(value) && value[index+1] >= 0x80 && value[index+1] <= 0x9f:
			index = consumeC1(value, index, value[index+1], 2)
		default:
			_, size := utf8.DecodeRuneInString(value[index:])
			output.WriteString(value[index : index+size])
			index += size
		}
	}
	return output.String()
}

func consumeEscape(value string, start int) int {
	if start+1 >= len(value) {
		return len(value)
	}
	switch value[start+1] {
	case '[':
		return consumeCSI(value, start+2)
	case ']', 'P', 'X', '^', '_':
		return consumeStringSequence(value, start+2)
	case '\\':
		return start + 2
	}

	index := start + 1
	for index < len(value) && value[index] >= 0x20 && value[index] <= 0x2f {
		index++
	}
	if index < len(value) && value[index] >= 0x30 && value[index] <= 0x7e {
		return index + 1
	}
	_, size := utf8.DecodeRuneInString(value[start+1:])
	return start + 1 + size
}

func consumeC1(value string, start int, control byte, width int) int {
	switch control {
	case 0x9b:
		return consumeCSI(value, start+width)
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
		return consumeStringSequence(value, start+width)
	default:
		return start + width
	}
}

func consumeCSI(value string, index int) int {
	for index < len(value) {
		if value[index] >= 0x40 && value[index] <= 0x7e {
			return index + 1
		}
		index++
	}
	return len(value)
}

func consumeStringSequence(value string, index int) int {
	for index < len(value) {
		switch {
		case value[index] == 0x07:
			return index + 1
		case value[index] == 0x1b && index+1 < len(value) && value[index+1] == '\\':
			return index + 2
		default:
			current, size := utf8.DecodeRuneInString(value[index:])
			if current == 0x9c || (current == utf8.RuneError && size == 1 && value[index] == 0x9c) {
				return index + size
			}
			index += size
		}
	}
	return len(value)
}
