package llm

import (
	"testing"
)

func TestTemperatureZeroPassedForLocalCompat(t *testing.T) {
	// isLocalCompatEndpoint returns true for non-openai.com / non-azure.com hosts.
	cases := []struct {
		name        string
		baseURL     string
		temperature float64
		reasoning   bool
		wantNil     bool // true if we expect temperature to NOT be sent
	}{
		{
			name:        "local compat temperature=0 must be forwarded (greedy decoding)",
			baseURL:     "http://192.168.2.73:1234/v1",
			temperature: 0,
			reasoning:   false,
			wantNil:     false,
		},
		{
			name:        "local compat temperature=0.7 forwarded",
			baseURL:     "http://localhost:1234/v1",
			temperature: 0.7,
			reasoning:   false,
			wantNil:     false,
		},
		{
			name:        "openai temperature=0 NOT forwarded (preserve server default)",
			baseURL:     "",
			temperature: 0,
			reasoning:   false,
			wantNil:     true,
		},
		{
			name:        "openai temperature=0.7 forwarded",
			baseURL:     "",
			temperature: 0.7,
			reasoning:   false,
			wantNil:     false,
		},
		{
			name:        "reasoning model: temperature never forwarded",
			baseURL:     "http://localhost:1234/v1",
			temperature: 0.5,
			reasoning:   true,
			wantNil:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localCompat := isLocalCompatEndpoint(tc.baseURL)

			var got *float32
			if !tc.reasoning {
				if localCompat || tc.temperature != 0 {
					v := float32(tc.temperature)
					got = &v
				}
			}

			if tc.wantNil && got != nil {
				t.Errorf("expected temperature=nil, got %v", *got)
			}
			if !tc.wantNil && got == nil {
				t.Errorf("expected temperature to be set, got nil (temperature=%v, localCompat=%v)", tc.temperature, localCompat)
			}
			if got != nil && *got != float32(tc.temperature) {
				t.Errorf("expected temperature=%v, got %v", tc.temperature, *got)
			}
		})
	}
}
