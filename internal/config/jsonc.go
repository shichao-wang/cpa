package config

import "bytes"

// Settings files are read as JSONC: comments and trailing commas are
// allowed, because a file whose whole job is to explain upstream routing is
// painful to maintain without them.
//
// Both passes below are string-aware. That matters more than it sounds:
// "http://127.0.0.1:8317" contains a // that is not a comment.

// stripJSONC removes // line comments, /* block */ comments and trailing
// commas from a JSON document.
func stripJSONC(data []byte) []byte {
	return stripTrailingCommas(stripComments(data))
}

func stripComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]

		if inString {
			out = append(out, c)
			switch c {
			case '\\':
				if i+1 < len(b) {
					out = append(out, b[i+1])
					i++
				}
			case '"':
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) {
			if b[i+1] == '/' {
				for i < len(b) && b[i] != '\n' {
					i++
				}
				if i < len(b) {
					out = append(out, '\n')
				}
				continue
			}
			if b[i+1] == '*' {
				i += 2
				for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
					i++
				}
				i++ // land on '/', the loop's i++ steps past it
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func stripTrailingCommas(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out = append(out, c)
			switch c {
			case '\\':
				if i+1 < len(b) {
					out = append(out, b[i+1])
					i++
				}
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && isSpace(b[j]) {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue // drop the comma, keep the whitespace
			}
		}
		out = append(out, c)
	}
	return out
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// HasComments reports whether data carries JSONC comments outside its
// strings. Callers that rewrite a settings file use this to warn that the
// comments will not survive the round trip.
func HasComments(data []byte) bool {
	return !bytes.Equal(stripComments(data), data)
}
