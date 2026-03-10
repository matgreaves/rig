package rigdata

import (
	"fmt"
	"time"
)

// FormatDuration formats a duration as seconds with 3 decimal places.
func FormatDuration(d time.Duration) string {
	secs := d.Seconds()
	return fmt.Sprintf("%.3fs", secs)
}

// FormatLatency formats milliseconds into a human-readable string.
func FormatLatency(ms float64) string {
	if ms < 1 {
		return fmt.Sprintf("%.0fµs", ms*1000)
	}
	if ms < 1000 {
		return fmt.Sprintf("%.1fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}

// FormatBytes formats byte counts into a compact human-readable string.
func FormatBytes(b int64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%dB", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	}
}

// FormatLsDuration formats a duration in milliseconds for ls/summary output.
func FormatLsDuration(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.2fs", ms/1000)
}
