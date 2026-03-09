package agent

import (
	"errors"
	"testing"
)

func TestIsMCPTransportError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "transport has been closed",
			err:  errors.New("failed to call mcp tool: transport error: transport has been closed"),
			want: true,
		},
		{
			name: "wrapped transport error",
			err:  errors.New("[NodeRunError] failed to invoke tool[name:GetLiveContext id:736747418]: failed to call mcp tool: transport error: transport has been closed\n------------------------\nnode path: [tools]"),
			want: true,
		},
		{
			name: "generic transport error",
			err:  errors.New("transport error: something went wrong"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMCPTransportError(tt.err)
			if got != tt.want {
				t.Errorf("isMCPTransportError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "429 in message",
			err:  errors.New("error, status code: 429, status: Too Many Requests, message: Rate limit exceeded"),
			want: true,
		},
		{
			name: "rate limit phrase",
			err:  errors.New("rate limit exceeded for model"),
			want: true,
		},
		{
			name: "too many requests",
			err:  errors.New("too many requests, please try again later"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRateLimitError(tt.err)
			if got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
