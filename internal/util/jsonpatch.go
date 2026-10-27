package util

import "bytes"

// ReplaceJSONStringField substitutes every JSON string value for key, leaving
// the rest of the document bytes unchanged. It walks the document token by
// token instead of searching for the raw key bytes, so text that merely
// mentions the key inside a JSON string (tool arguments, user content, ...) is
// never touched.
//
// The bool reports whether anything was replaced.
func ReplaceJSONStringField(data []byte, key, value string) ([]byte, bool) {
	quoted, err := Marshal(value)
	if err != nil {
		return nil, false
	}

	type span struct{ start, end int }
	spans := make([]span, 0, 1)
	for i := 0; i < len(data); {
		if data[i] != '"' {
			i++
			continue
		}
		end, ok := skipJSONString(data, i)
		if !ok {
			return nil, false
		}
		// A string token is an object key only when a colon follows it. Reading
		// the raw key is safe here: escape sequences cannot spell an ASCII key
		// without extra bytes, so an escaped key simply stays untouched.
		if j := skipJSONSpace(data, end); j < len(data) && data[j] == ':' &&
			string(data[i+1:end-1]) == key {
			start := skipJSONSpace(data, j+1)
			if start < len(data) && data[start] == '"' {
				valueEnd, ok := skipJSONString(data, start)
				if !ok {
					return nil, false
				}
				if !bytes.Equal(data[start:valueEnd], quoted) {
					spans = append(spans, span{start, valueEnd})
				}
				i = valueEnd
				continue
			}
		}
		i = end
	}
	if len(spans) == 0 {
		return nil, false
	}

	out := make([]byte, 0, len(data)+len(spans)*(len(quoted)-2))
	last := 0
	for _, replacement := range spans {
		out = append(out, data[last:replacement.start]...)
		out = append(out, quoted...)
		last = replacement.end
	}
	return append(out, data[last:]...), true
}

func skipJSONSpace(data []byte, start int) int {
	for start < len(data) && isJSONSpace(data[start]) {
		start++
	}
	return start
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func skipJSONString(data []byte, start int) (int, bool) {
	if start >= len(data) || data[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}
