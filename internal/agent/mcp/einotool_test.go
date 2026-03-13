package mcp

import (
	"strings"
	"testing"
)

func TestNormalizeBooleanSchemas(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		noRawBool   bool   // normalized output must not contain schema booleans
		contains    string // normalized output must contain this (e.g. "type")
		notContains string // normalized output must not contain this
	}{
		{
			name:        "properties with true",
			input:       `{"type":"object","properties":{"foo":true,"bar":{"type":"string"}}}`,
			noRawBool:   true,
			contains:    `"type":"object"`,
			notContains: `"foo":true`,
		},
		{
			name:        "top-level true",
			input:       `true`,
			noRawBool:   true,
			contains:    `"type":"object"`,
			notContains: "",
		},
		{
			name:        "top-level false",
			input:       `false`,
			noRawBool:   true,
			contains:    `"description":"unsupported"`,
			notContains: "",
		},
		{
			name:        "additionalProperties true",
			input:       `{"type":"object","additionalProperties":true}`,
			noRawBool:   true,
			contains:    `"additionalProperties"`,
			notContains: `"additionalProperties":true`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBooleanSchemas([]byte(tt.input))
			if err != nil {
				t.Fatalf("normalizeBooleanSchemas: %v", err)
			}
			gotStr := string(got)
			if tt.noRawBool && (strings.Contains(gotStr, `:true`) || strings.Contains(gotStr, `:false`)) {
				// Exclude legitimate "true"/"false" inside strings
				if strings.Contains(gotStr, `"true"`) || strings.Contains(gotStr, `"false"`) {
					// might be enum or default; allow
				} else {
					t.Errorf("normalized schema should not contain raw boolean schema; got %s", gotStr)
				}
			}
			if tt.contains != "" && !strings.Contains(gotStr, tt.contains) {
				t.Errorf("got %s, expected to contain %q", gotStr, tt.contains)
			}
			if tt.notContains != "" && strings.Contains(gotStr, tt.notContains) {
				t.Errorf("got %s, expected not to contain %q", gotStr, tt.notContains)
			}
		})
	}
}
