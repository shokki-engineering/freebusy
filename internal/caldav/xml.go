package caldav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// XML namespaces CalDAV speaks: WebDAV's, CalDAV's, and Apple's
// CalendarServer extensions, whose getctag every desktop client polls.
const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCS     = "http://calendarserver.org/ns/"
)

// maxBody caps a request body. CalDAV bodies are small XML documents or one
// calendar object; this is generous for either.
const maxBody = 1 << 20

// node is a parsed XML element, generic enough for every request body.
type node struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []node     `xml:",any"`
	Text    string     `xml:",chardata"`
}

// readXML parses the request body, or returns nil for an empty one.
func readXML(r *http.Request) (*node, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, failf(http.StatusRequestEntityTooLarge, "", "request body over %d bytes", maxBody)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	var n node
	if err := xml.Unmarshal(raw, &n); err != nil {
		return nil, failf(http.StatusBadRequest, "", "malformed XML body: %v", err)
	}
	return &n, nil
}

func (n *node) is(space, local string) bool {
	return n != nil && n.XMLName.Space == space && n.XMLName.Local == local
}

// child returns n's first child named space:local, or nil.
func (n *node) child(space, local string) *node {
	if n == nil {
		return nil
	}
	for i := range n.Nodes {
		if n.Nodes[i].is(space, local) {
			return &n.Nodes[i]
		}
	}
	return nil
}

func (n *node) attr(local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// propNames lists the property names inside a DAV:prop element.
func (n *node) propNames() []xml.Name {
	var out []xml.Name
	for _, c := range n.Nodes {
		out = append(out, c.XMLName)
	}
	return out
}

// prefixes are the namespaces the writer declares on the root element.
var prefixes = map[string]string{nsDAV: "D", nsCalDAV: "C", nsCS: "CS"}

// open and close render an element tag, declaring an unknown namespace on
// the element itself.
func open(name xml.Name) string {
	if p, ok := prefixes[name.Space]; ok {
		return "<" + p + ":" + name.Local + ">"
	}
	if name.Space == "" {
		return "<" + name.Local + ">"
	}
	return `<X:` + name.Local + ` xmlns:X="` + escapeAttr(name.Space) + `">`
}

func closeTag(name xml.Name) string {
	if p, ok := prefixes[name.Space]; ok {
		return "</" + p + ":" + name.Local + ">"
	}
	if name.Space == "" {
		return "</" + name.Local + ">"
	}
	return "</X:" + name.Local + ">"
}

func empty(name xml.Name) string {
	s := open(name)
	return s[:len(s)-1] + "/>"
}

// prop is one property and its value, already rendered as XML content.
type prop struct {
	name  xml.Name
	inner string
}

// response is one DAV:response of a multistatus: props found, props asked
// for and absent, or a status for the whole resource.
type response struct {
	href    string
	found   []prop
	missing []xml.Name
	failed  map[int][]xml.Name // PROPPATCH: status -> props
	status  int                // non-zero: a status-only response
	body    string             // extra content inside the response (REPORT errors)
}

// writeMultistatus renders responses as a 207 Multi-Status, RFC 4918
// section 13.
func writeMultistatus(w http.ResponseWriter, responses []response) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">`)
	for _, r := range responses {
		b.WriteString("<D:response><D:href>" + escapeText(r.href) + "</D:href>")
		if r.status != 0 {
			b.WriteString(statusLine(r.status))
		}
		if len(r.found) > 0 {
			b.WriteString("<D:propstat><D:prop>")
			for _, p := range r.found {
				if p.inner == "" {
					b.WriteString(empty(p.name))
				} else {
					b.WriteString(open(p.name) + p.inner + closeTag(p.name))
				}
			}
			b.WriteString("</D:prop>" + statusLine(http.StatusOK) + "</D:propstat>")
		}
		if len(r.missing) > 0 {
			b.WriteString(propstat(r.missing, http.StatusNotFound))
		}
		for status, names := range r.failed {
			b.WriteString(propstat(names, status))
		}
		b.WriteString(r.body)
		b.WriteString("</D:response>")
	}
	b.WriteString("</D:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

func propstat(names []xml.Name, status int) string {
	var b strings.Builder
	b.WriteString("<D:propstat><D:prop>")
	for _, n := range names {
		b.WriteString(empty(n))
	}
	b.WriteString("</D:prop>" + statusLine(status) + "</D:propstat>")
	return b.String()
}

func statusLine(code int) string {
	return fmt.Sprintf("<D:status>HTTP/1.1 %d %s</D:status>", code, http.StatusText(code))
}

func hrefXML(href string) string { return "<D:href>" + escapeText(href) + "</D:href>" }

func escapeText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func escapeAttr(s string) string { return strings.ReplaceAll(escapeText(s), `"`, "&quot;") }

// errNoBody marks a request that needed an XML body and sent none.
var errNoBody = errors.New("missing XML body")
