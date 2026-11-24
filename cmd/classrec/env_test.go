package main

import "testing"

func TestParseKeyLine(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"SARVAM_API_KEY=abc123", "abc123", true},
		{`export SARVAM_API_KEY="abc 123"`, "abc 123", true},
		{"  SARVAM_API_KEY='abc'  ", "abc", true},
		{"SARVAM_API_KEY=", "", false},
		{"# SARVAM_API_KEY=abc", "", false},
		{"OTHER_KEY=abc", "", false},
	}
	for _, c := range cases {
		if got, ok := parseKeyLine(c.line); got != c.want || ok != c.ok {
			t.Errorf("parseKeyLine(%q) = %q, %v; want %q, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}
