package service

import "testing"

func TestTruncateBody(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxRunes: 140,
			want:     "hello",
		},
		{
			name:     "exact fit no truncation",
			input:    "12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
			maxRunes: 140,
			want:     "12345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
		},
		{
			name:     "ASCII truncation with suffix",
			input:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 146 'a's
			maxRunes: 140,
			// budget = 140 - 13 (suffix) = 127
			want: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + truncateSuffix,
		},
		{
			name:     "Unicode rune-aware truncation",
			input:    "你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界", // 150 runes
			maxRunes: 140,
			// budget = 140 - 13 = 127, 127 = 31*4 + 3 = "你好世界"*31 + "你好世"
			want: "你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世界你好世" + truncateSuffix,
		},
		{
			name:     "empty string unchanged",
			input:    "",
			maxRunes: 140,
			want:     "",
		},
		{
			name:     "zero maxRunes drops suffix",
			input:    "hello world",
			maxRunes: 0,
			want:     "",
		},
		{
			name:     "result never exceeds maxRunes",
			input:    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEF",
			maxRunes: 140,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateBody(tc.input, tc.maxRunes)
			if tc.want != "" && got != tc.want {
				t.Errorf("truncateBody(%q, %d) = %q; want %q", tc.input, tc.maxRunes, got, tc.want)
			}
			if tc.name == "result never exceeds maxRunes" {
				if len([]rune(got)) > tc.maxRunes {
					t.Errorf("truncateBody(%q, %d) returned %d runes, exceeding maxRunes limit", tc.input, tc.maxRunes, len([]rune(got)))
				}
			}
		})
	}
}
