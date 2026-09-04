package config

import (
	"bytes"
	"io"
)

// newCommentStripper returns a reader over JSON with // line comments removed,
// so config files can be annotated. Comment markers inside string literals are
// preserved.
func newCommentStripper(src []byte) io.Reader {
	var out bytes.Buffer
	out.Grow(len(src))

	inString, escaped, inComment := false, false, false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		switch {
		case inComment:
			if ch == '\n' {
				inComment = false
				out.WriteByte(ch)
			}
		case inString:
			out.WriteByte(ch)
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
		case ch == '"':
			inString = true
			out.WriteByte(ch)
		case ch == '/' && i+1 < len(src) && src[i+1] == '/':
			inComment = true
			i++
		default:
			out.WriteByte(ch)
		}
	}
	return bytes.NewReader(out.Bytes())
}
