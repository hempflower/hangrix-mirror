package config

import (
	"reflect"
	"testing"
)

func TestParseMcpServers(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "single", raw: "playwright", want: []string{"playwright"}},
		{name: "two comma-separated", raw: "playwright,github", want: []string{"playwright", "github"}},
		{name: "with spaces", raw: " playwright , github ", want: []string{"playwright", "github"}},
		{name: "trailing comma", raw: "playwright,", want: []string{"playwright"}},
		{name: "leading comma", raw: ",playwright", want: []string{"playwright"}},
		{name: "empty middle", raw: "a,,b", want: []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMcpServers(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMcpServers(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseIntDefault(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		def  int
		want int
	}{
		{name: "empty uses default", raw: "", def: 200, want: 200},
		{name: "whitespace uses default", raw: "  ", def: 200, want: 200},
		{name: "normal", raw: "30", def: 200, want: 30},
		{name: "zero", raw: "0", def: 200, want: 0},
		{name: "negative", raw: "-1", def: 200, want: -1},
		{name: "garbage falls back", raw: "abc", def: 200, want: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIntDefault(tt.raw, tt.def)
			if got != tt.want {
				t.Errorf("parseIntDefault(%q, %d) = %d, want %d", tt.raw, tt.def, got, tt.want)
			}
		})
	}
}

func TestClampNonNegative(t *testing.T) {
	tests := []struct {
		name string
		v    int
		want int
	}{
		{name: "positive unchanged", v: 5, want: 5},
		{name: "zero unchanged", v: 0, want: 0},
		{name: "negative one becomes zero", v: -1, want: 0},
		{name: "negative large becomes zero", v: -100, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampNonNegative(tt.v)
			if got != tt.want {
				t.Errorf("clampNonNegative(%d) = %d, want %d", tt.v, got, tt.want)
			}
		})
	}
}
