package tools

import (
	"context"
	"testing"
	"time"
)

func TestExecDateTime_SystemTimezone(t *testing.T) {
	result, err := ExecDateTime(context.Background(), DateTimeParams{})
	if err != nil {
		t.Fatalf("ExecDateTime: %v", err)
	}

	now := time.Now()
	if result.Date != now.Format("2006-01-02") {
		t.Errorf("date mismatch: got %s", result.Date)
	}
	if result.Weekday != now.Weekday().String() {
		t.Errorf("weekday mismatch: got %s, want %s", result.Weekday, now.Weekday().String())
	}
	if result.Timezone == "" {
		t.Error("expected non-empty timezone")
	}
	if result.UTCOffset == "" {
		t.Error("expected non-empty UTC offset")
	}
	if result.UnixTimestamp == 0 {
		t.Error("expected non-zero unix timestamp")
	}
	if result.OS == "" {
		t.Error("expected non-empty OS")
	}
}

func TestExecDateTime_ExplicitTimezone(t *testing.T) {
	result, err := ExecDateTime(context.Background(), DateTimeParams{Timezone: "Asia/Tokyo"})
	if err != nil {
		t.Fatalf("ExecDateTime: %v", err)
	}
	if result.Timezone != "Asia/Tokyo" {
		t.Errorf("expected Asia/Tokyo, got %s", result.Timezone)
	}
	if result.UTCOffset != "+09:00" {
		t.Errorf("expected +09:00, got %s", result.UTCOffset)
	}
}

func TestExecDateTime_InvalidTimezone(t *testing.T) {
	_, err := ExecDateTime(context.Background(), DateTimeParams{Timezone: "Not/A/Timezone"})
	if err == nil {
		t.Error("expected error for invalid timezone")
	}
}
