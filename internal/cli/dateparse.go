package cli

import (
	"fmt"
	"strings"
	"time"
)

// parseDate accepts a generous variety of formats so users don't have to
// memorize one. Returned times are in the local timezone unless the input
// included an explicit zone.
//
// Accepted forms (examples):
//
//	2025-01-31
//	2025/01/31
//	2025-01-31 14:00
//	2025-01-31T14:00:00Z
//	yesterday, today, now
//	7d, 2w, 3m, 1y     (relative offsets, interpreted as "ago")
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}

	switch strings.ToLower(s) {
	case "now":
		return time.Now(), nil
	case "today":
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		now := time.Now().AddDate(0, 0, -1)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	}

	// Relative offsets: 7d, 2w, 3m, 1y, 12h.
	if d, ok := parseRelative(s); ok {
		return time.Now().Add(-d), nil
	}

	layouts := []string{
		"2006-01-02",
		"2006/01/02",
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
		"02 Jan 2006",
		"02 Jan 2006 15:04",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q", s)
}

func parseRelative(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	var n int
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil {
		return 0, false
	}
	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, true
	case 'm':
		return time.Duration(n) * 30 * 24 * time.Hour, true
	case 'y':
		return time.Duration(n) * 365 * 24 * time.Hour, true
	}
	return 0, false
}
