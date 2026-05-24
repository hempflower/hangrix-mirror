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
