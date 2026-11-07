package dryrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const redacted = "[redacted]"

// sensitiveFragments mark a name (JSON key, query parameter, variable) whose
// value must never be echoed to an agent. Over-matching is the safe side.
var sensitiveFragments = []string{
	"token", "secret", "password", "passwd", "authorization", "apikey", "api_key",
	"api-key", "cookie", "credential", "private_key", "privatekey", "signature",
	"jwt", "bearer", "session",
}

func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, f := range sensitiveFragments {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

// redactURL masks the password and sensitive query values of u.
func redactURL(u *url.URL) string {
	c := *u
	if c.User != nil {
		if _, ok := c.User.Password(); ok {
			c.User = url.UserPassword(c.User.Username(), "xxxxx")
		}
	}
	if c.RawQuery != "" {
		q := c.Query()
		for k, vs := range q {
			if sensitiveName(k) {
				for i := range vs {
					vs[i] = redacted
				}
			}
		}
		c.RawQuery = q.Encode()
	}
	return c.String()
}

// redactJSON rewrites a JSON document compactly with the scalar values under
// sensitive keys masked, keeping key order. ok is false if body is not valid JSON.
func redactJSON(body []byte) (out []byte, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := walkJSON(dec, &buf, false); err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return buf.Bytes(), true
}

func walkJSON(dec *json.Decoder, out *bytes.Buffer, mask bool) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		if mask && tok != nil {
			tok = redacted
		}
		b, err := json.Marshal(tok)
		if err != nil {
			return err
		}
		out.Write(b)
		return nil
	}

	switch d {
	case '{':
		out.WriteByte('{')
		for first := true; dec.More(); first = false {
			if !first {
				out.WriteByte(',')
			}
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			kb, _ := json.Marshal(key)
			out.Write(kb)
			out.WriteByte(':')
			if err := walkJSON(dec, out, mask || sensitiveName(key)); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case '[':
		out.WriteByte('[')
		for first := true; dec.More(); first = false {
			if !first {
				out.WriteByte(',')
			}
			if err := walkJSON(dec, out, mask); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	}
	_, err = dec.Token() // closing delimiter
	return err
}

var textSecret = regexp.MustCompile(
	`(?i)((?:token|secret|password|passwd|api[_-]?key|authorization|cookie|signature|jwt)["']?\s*[:=]\s*["']?(?:(?:bearer|basic)\s+)?)([^\s"'&,;}]+)`)

func redactText(s string) string {
	return textSecret.ReplaceAllString(s, "${1}"+redacted)
}

func isBinary(contentType string, sample []byte) bool {
	ct := strings.ToLower(contentType)
	for _, p := range []string{"image/", "audio/", "video/", "font/", "application/octet-stream",
		"application/pdf", "application/zip", "application/gzip", "application/x-"} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(sample)
}

// preview renders body for an agent: redacted, binary summarised, cut to max
// bytes on a rune boundary. truncated reports whether anything was cut.
func preview(body []byte, contentType string, max int) (text string, truncated bool) {
	if len(body) == 0 {
		return "", false
	}
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	if isBinary(contentType, head) {
		ct := contentType
		if ct == "" {
			ct = "unknown content-type"
		}
		return fmt.Sprintf("<%d bytes, %s>", len(body), ct), false
	}

	var s string
	if red, ok := redactJSON(body); ok {
		s = string(red)
	} else {
		s = redactText(string(body))
	}
	if max <= 0 {
		return "", len(s) > 0
	}
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
