package tools

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// DateTimeParams defines the input schema for the get_datetime tool.
type DateTimeParams struct {
	Timezone string `json:"timezone" jsonschema:"description=IANA timezone name (e.g. America/New_York). If omitted the system timezone is used."`
}

// DateTimeResult is the output of the get_datetime tool.
type DateTimeResult struct {
	DateTime       string `json:"datetime"`        // RFC 3339
	Date           string `json:"date"`             // YYYY-MM-DD
	Time           string `json:"time"`             // HH:MM:SS
	Weekday        string `json:"weekday"`          // Monday, Tuesday, ...
	Timezone       string `json:"timezone"`         // IANA name
	UTCOffset      string `json:"utc_offset"`       // e.g. -06:00
	UnixTimestamp  int64  `json:"unix_timestamp"`
	OS             string `json:"os"`               // runtime.GOOS
}

// ExecDateTime returns the current date, time, and system timezone.
func ExecDateTime(_ context.Context, params DateTimeParams) (*DateTimeResult, error) {
	now := time.Now()

	if params.Timezone != "" {
		loc, err := time.LoadLocation(params.Timezone)
		if err != nil {
			return nil, err
		}
		now = now.In(loc)
	}

	zone, offset := now.Zone()
	_ = zone
	hours := offset / 3600
	mins := (offset % 3600) / 60
	if mins < 0 {
		mins = -mins
	}

	sign := "+"
	if hours < 0 {
		sign = "-"
		hours = -hours
	}

	tzName := now.Location().String()

	return &DateTimeResult{
		DateTime:      now.Format(time.RFC3339),
		Date:          now.Format("2006-01-02"),
		Time:          now.Format("15:04:05"),
		Weekday:       now.Weekday().String(),
		Timezone:      tzName,
		UTCOffset:     sign + fmt.Sprintf("%02d:%02d", hours, mins),
		UnixTimestamp: now.Unix(),
		OS:            runtime.GOOS,
	}, nil
}
