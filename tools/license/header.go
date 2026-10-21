// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"regexp"
	"strings"
)

// style is a comment syntax: each header line is open + text + close.
type style struct {
	open, close string
}

// header is the two-line header for license, newline-terminated.
func (s style) header(license string) []byte {
	return []byte(s.line("Copyright "+Year+" "+Holder) + "\n" + s.line("SPDX-License-Identifier: "+license) + "\n")
}

func (s style) line(text string) string { return s.open + text + s.close }

// copyrightLine matches this file's copyright line in style s, whatever the
// year or range.
func (s style) copyrightLine() *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(s.open) + `Copyright \d{4}(-\d{4})? ` + regexp.QuoteMeta(Holder) + regexp.QuoteMeta(s.close) + `$`)
}

// lines splits src into lines without their terminators.
func lines(src []byte) []string {
	return strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
}

// body is where the header goes: after a shebang, else at the top.
func body(ls []string) int {
	if len(ls) > 0 && strings.HasPrefix(ls[0], "#!") {
		return 1
	}
	return 0
}

// hasHeader reports whether src opens -- after a shebang, if any -- with the
// header want, any year or range accepted on the copyright line.
func hasHeader(src []byte, s style, want []byte) bool {
	ls := lines(src)
	at := body(ls)
	wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	if len(ls) < at+2 {
		return false
	}
	return s.copyrightLine().MatchString(ls[at]) && ls[at+1] == wantLines[1]
}

// foreign matches a copyright or SPDX line of any holder or licence: what
// withHeader replaces when it heads a file.
var foreign = regexp.MustCompile(`(?i)^\s*(//|#|--|<!--)\s*(copyright\b|spdx-license-identifier:)`)

// withHeader returns src with want at its head: after a shebang, replacing
// any copyright or SPDX lines already in the file's opening comment, and
// followed by a blank line so the header never becomes a Go package's doc
// comment or runs into a Markdown heading.
func withHeader(src []byte, s style, want []byte) []byte {
	ls := lines(src)
	at := body(ls)
	rest := ls[at:]
	// Drop copyright and SPDX lines from the opening run of lines -- another
	// holder's header, or this one's with a wrong licence -- and the blank
	// line that followed them.
	i := 0
	for i < len(rest) && foreign.MatchString(rest[i]) {
		i++
	}
	if i > 0 && i < len(rest) && strings.TrimSpace(rest[i]) == "" {
		i++
	}
	rest = rest[i:]

	var b bytes.Buffer
	for _, l := range ls[:at] {
		b.WriteString(l + "\n")
	}
	b.Write(want)
	if len(rest) > 0 && strings.TrimSpace(rest[0]) != "" {
		b.WriteString("\n")
	}
	b.WriteString(strings.Join(rest, "\n"))
	return b.Bytes()
}
