package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eino-contrib/jsonschema"
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

func TestFixBooleanSchemaNodes(t *testing.T) {
	tests := []struct {
		name     string
		input    string // JSON schema with potential boolean sub-schemas
		checkNot string // final marshaled output must NOT contain this
	}{
		{
			name:     "additionalProperties boolean schema survives unmarshal",
			input:    `{"type":"object","additionalProperties":true}`,
			checkNot: `"additionalProperties":true`,
		},
		{
			name:     "nested boolean in anyOf",
			input:    `{"type":"object","properties":{"foo":{"anyOf":[{"type":"string"},true]}}}`,
			checkNot: `:true`,
		},
		{
			name:     "false schema replaced",
			input:    `{"type":"object","additionalProperties":false}`,
			checkNot: `"additionalProperties":false`,
		},
		{
			name:  "normal schema unchanged",
			input: `{"type":"object","properties":{"name":{"type":"string"}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &jsonschema.Schema{}
			if err := json.Unmarshal([]byte(tt.input), s); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			fixed := fixBooleanSchemaNodes(s)
			out, err := json.Marshal(fixed)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			outStr := string(out)
			if tt.checkNot != "" && strings.Contains(outStr, tt.checkNot) {
				t.Errorf("output should not contain %q; got %s", tt.checkNot, outStr)
			}
		})
	}
}
