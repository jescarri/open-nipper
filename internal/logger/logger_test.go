package logger

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestNew_DefaultLevel(t *testing.T) {
	t.Setenv("NIPPER_LOG_LEVEL", "")
	log, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if log == nil {
		t.Fatal("New() returned nil logger")
	}
}

func TestNew_DebugLevel(t *testing.T) {
	t.Setenv("NIPPER_LOG_LEVEL", "debug")
	log, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if !log.Core().Enabled(zapcore.DebugLevel) {
		t.Error("expected debug level to be enabled")
	}
}

func TestNew_InvalidLevel(t *testing.T) {
	t.Setenv("NIPPER_LOG_LEVEL", "trace")
	_, err := New()
	if err == nil {
		t.Error("expected error for unknown log level")
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		input string
		want  zapcore.Level
		err   bool
	}{
		{"", zapcore.InfoLevel, false},
		{"info", zapcore.InfoLevel, false},
		{"INFO", zapcore.InfoLevel, false},
		{"debug", zapcore.DebugLevel, false},
		{"warn", zapcore.WarnLevel, false},
		{"warning", zapcore.WarnLevel, false},
		{"error", zapcore.ErrorLevel, false},
		{"bogus", zapcore.InfoLevel, true},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got, err := parseLevel(c.input)
			if (err != nil) != c.err {
				t.Errorf("parseLevel(%q) error = %v, wantErr %v", c.input, err, c.err)
			}
			if !c.err && got != c.want {
				t.Errorf("parseLevel(%q) = %v, want %v", c.input, got, c.want)
			}
		})
	}
}

func TestWithMessage(t *testing.T) {
	log, err := New()
	if err != nil {
		t.Fatal(err)
	}
	child := WithMessage(log, "user-01", "sk-abc", "whatsapp", "trace-xyz")
	if child == nil {
		t.Error("WithMessage returned nil")
	}
}
