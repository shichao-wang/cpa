package config

import (
	"bytes"
	"encoding/json"
)

// Settings files are strict JSON. Field semantics live in the JSON Schema at
// examples/settings.schema.json, not in prose inside the file, so nothing
// here is used to parse.
//
// Earlier versions accepted JSONC (comments and trailing commas). These
// scanners survive only to recognise a file written in that older style, so
// the error can say what is actually wrong instead of reporting a stray "/".
// They are a migration aid and can go once such files are rare.
//
// Both passes are string-aware. That matters: "http://127.0.0.1:8317"
// contains a // that is not a comment.

// looksLikeJSONC reports whether data would parse once comments and trailing
// commas were removed, i.e. whether the failure is the format change rather
// than a genuine syntax error.
func looksLikeJSONC(data []byte) bool {
	stripped := stripTrailingCommas(stripComments(data))
	if bytes.Equal(stripped, data) {
		return false // nothing to strip; the error is something else
	}
	return json.Valid(stripped)
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
