package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Status of an upsert into a JSON config file.
const (
	spliceCreated   = "created"
	spliceAdded     = "added"
	spliceUnchanged = "unchanged"
	spliceDiffers   = "differs"
	spliceReplaced  = "replaced"
)

// jsonMember is one member of a JSON object: its key and the byte range of its value.
type jsonMember struct {
	key        string
	start, end int
}

// objectMembers lists the members of the JSON object that begins at or after
// data[base]. open is the index of its '{' and close that of its '}'.
func objectMembers(data []byte, base int) (members []jsonMember, open, close int, err error) {
	open = base
	for open < len(data) && isJSONSpace(data[open]) {
		open++
	}
	if open >= len(data) || data[open] != '{' {
		return nil, 0, 0, errors.New("not a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(data[open:]))
	if _, err := dec.Token(); err != nil {
		return nil, 0, 0, err
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, 0, 0, err
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, 0, err
		}
		end := open + int(dec.InputOffset())
		members = append(members, jsonMember{key: key, start: end - len(raw), end: end})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, 0, err
	}
	return members, open, open + int(dec.InputOffset()) - 1, nil
}

func isJSONSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func indentJSON(v []byte, prefix string) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, v, prefix, "  "); err != nil {
		return v
	}
	return buf.Bytes()
}

func sameJSON(a, b []byte) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// lineIndent returns the leading whitespace of the line holding data[pos], and
// whether that line starts after a newline (false for a one-line object).
func lineIndent(data []byte, pos int) (string, bool) {
	start := bytes.LastIndexByte(data[:pos], '\n') + 1
	if start == 0 {
		return "", false
	}
	end := start
	for end < pos && (data[end] == ' ' || data[end] == '\t') {
		end++
	}
	return string(data[start:end]), true
}

// insertMember appends `"key": value` as the last member of the object closing at
// data[close]. Whitespace already in the file is kept; the new member copies the
// indentation of the one before it.
func insertMember(data []byte, members []jsonMember, close int, key string, value []byte, depth int) []byte {
	k, _ := json.Marshal(key)
	tail := close
	for tail > 0 && isJSONSpace(data[tail-1]) {
		tail--
	}
	pad := strings.Repeat("  ", depth)
	multiline := true
	if len(members) > 0 {
		pad, multiline = lineIndent(data, members[len(members)-1].start)
	}
	var b bytes.Buffer
	b.Write(data[:tail])
	if len(members) > 0 {
		b.WriteByte(',')
	}
	if !multiline {
		var compact bytes.Buffer
		_ = json.Compact(&compact, value)
		b.WriteByte(' ')
		b.Write(k)
		b.WriteString(": ")
		b.Write(compact.Bytes())
		b.Write(data[tail:])
		return b.Bytes()
	}
	b.WriteByte('\n')
	b.WriteString(pad)
	b.Write(k)
	b.WriteString(": ")
	b.Write(indentJSON(value, pad))
	if len(members) == 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat("  ", depth-1))
		b.Write(data[close:])
		return b.Bytes()
	}
	b.Write(data[tail:])
	return b.Bytes()
}

// upsertEntry sets container[key] = entry in a JSON object file, leaving every
// other byte of the file as it was. Without force an existing, different entry is
// kept and reported as spliceDiffers. A file that is not a JSON object is an error.
func upsertEntry(data []byte, container, key string, entry []byte, force bool) ([]byte, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		inner := insertMember([]byte("{}"), nil, 1, key, entry, 2)
		out := insertMember([]byte("{}"), nil, 1, container, inner, 1)
		return append(out, '\n'), spliceCreated, nil
	}
	if !json.Valid(data) {
		return nil, "", errors.New("is not valid JSON")
	}
	top, _, topClose, err := objectMembers(data, 0)
	if err != nil {
		return nil, "", fmt.Errorf("is not a JSON object: %w", err)
	}
	var cm *jsonMember
	for i := range top {
		if top[i].key == container {
			cm = &top[i]
		}
	}
	if cm == nil {
		inner := insertMember([]byte("{}"), nil, 1, key, entry, 2)
		return insertMember(data, top, topClose, container, inner, 1), spliceAdded, nil
	}
	inner, _, innerClose, err := objectMembers(data, cm.start)
	if err != nil || cm.start >= len(data) || data[cm.start] != '{' {
		return nil, "", fmt.Errorf("%q is not a JSON object", container)
	}
	for _, m := range inner {
		if m.key != key {
			continue
		}
		if sameJSON(data[m.start:m.end], entry) {
			return data, spliceUnchanged, nil
		}
		if !force {
			return data, spliceDiffers, nil
		}
		var b bytes.Buffer
		b.Write(data[:m.start])
		b.Write(indentJSON(entry, "    "))
		b.Write(data[m.end:])
		return b.Bytes(), spliceReplaced, nil
	}
	return insertMember(data, inner, innerClose, key, entry, 2), spliceAdded, nil
}
