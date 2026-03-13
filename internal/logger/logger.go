// Package logger provides Zap-based structured logging initialization for Open-Nipper.
package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New initializes a production zap.Logger.
// The log level is read from the NIPPER_LOG_LEVEL environment variable (default: info).
// The log format is read from the NIPPER_LOG_FORMAT environment variable (default: json).
// The logger includes static fields: service and env.
func New() (*zap.Logger, error) {
	return NewWithService("open-nipper-gateway")
}

// NewWithService initializes a production zap.Logger for the provided service name.
// The encoding format is controlled by the NIPPER_LOG_FORMAT environment variable:
// "json" (default) or "text" (console-friendly, human-readable).
func NewWithService(service string) (*zap.Logger, error) {
	level, err := parseLevel(os.Getenv("NIPPER_LOG_LEVEL"))
	if err != nil {
		return nil, err
	}

	encoding := parseFormat(os.Getenv("NIPPER_LOG_FORMAT"))

	var encCfg zapcore.EncoderConfig
	if encoding == "console" {
		encCfg = zap.NewDevelopmentEncoderConfig()
	} else {
		encCfg = zap.NewProductionEncoderConfig()
	}
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      false,
		Encoding:         encoding,
		EncoderConfig:    encCfg,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	base, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("logger: build: %w", err)
	}

	env := os.Getenv("NIPPER_ENV")
	if env == "" {
		env = "production"
	}

	if strings.TrimSpace(service) == "" {
		service = "open-nipper"
	}

	return base.With(
		zap.String("service", service),
		zap.String("env", env),
	), nil
}

// parseFormat converts a format string to a zap encoding name.
// Accepts "json" (default) or "text"/"console" for human-readable output.
func parseFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "text", "console":
		return "console"
	default:
		return "json"
	}
}

// parseLevel converts a level string to zapcore.Level.
// Defaults to InfoLevel when s is empty.
func parseLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return zap.InfoLevel, nil
	case "debug":
		return zap.DebugLevel, nil
	case "warn", "warning":
		return zap.WarnLevel, nil
	case "error":
		return zap.ErrorLevel, nil
	default:
		return zap.InfoLevel, fmt.Errorf("logger: unknown log level %q", s)
	}
}

// WithMessage appends standard message-scoped fields to a logger.
func WithMessage(log *zap.Logger, userID, sessionKey, channelType, traceID string) *zap.Logger {
	fields := make([]zap.Field, 0, 4)
	if userID != "" {
		fields = append(fields, zap.String("userId", userID))
	}
	if sessionKey != "" {
		fields = append(fields, zap.String("sessionKey", sessionKey))
	}
	if channelType != "" {
		fields = append(fields, zap.String("channelType", channelType))
	}
	if traceID != "" {
		fields = append(fields, zap.String("traceId", traceID))
	}
	return log.With(fields...)
}
