package local

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

func TestXmlEscapeAttr(t *testing.T) {
	t.Parallel()

	type attrCase struct {
		input, want string
	}
	cases := []attrCase{
		{"", ""},
		{"plain", "plain"},
		{`he said "go"`, "he said &quot;go&quot;"},
		{"it's done", "it&apos;s done"},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{`"double" & 'single'`, "&quot;double&quot; &amp; &apos;single&apos;"},
		{"&amp;", "&amp;amp;"},
	}

	type elem struct {
		A string `xml:"a,attr"`
	}

	for _, tc := range cases {
		got := xmlEscapeAttr(tc.input)
		if got != tc.want {
			t.Errorf("xmlEscapeAttr(%q) = %q, want %q", tc.input, got, tc.want)
		}

		xmlStr := fmt.Sprintf(`<e a="%s"/>`, got)
		var decoded elem
		if err := xml.NewDecoder(strings.NewReader(xmlStr)).Decode(&decoded); err != nil {
			t.Errorf("xmlEscapeAttr(%q): generated invalid XML attribute: %v\n  XML: %s", tc.input, err, xmlStr)
		} else if decoded.A != tc.input {
			t.Errorf("xmlEscapeAttr(%q): round-trip mismatch: decoded %q, want %q", tc.input, decoded.A, tc.input)
		}
	}
}
