/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

// Package cron provides a minimal 5-field cron expression parser.
// Fields: minute hour day_of_month month day_of_week
// Supports: *, specific values, ranges (1-5), steps (*/5), lists (1,3,5)
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Validate checks whether a cron expression is syntactically valid.
// It returns an error describing the problem, or nil if valid.
func Validate(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("must have 5 fields (minute hour day_of_month month day_of_week), got %d", len(fields))
	}

	names := []string{"minute", "hour", "day_of_month", "month", "day_of_week"}
	limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

	for i, field := range fields {
		if err := validateField(field, limits[i][0], limits[i][1]); err != nil {
			return fmt.Errorf("field %d (%s): %w", i+1, names[i], err)
		}
	}
	return nil
}

func validateField(field string, min, max int) error {
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("empty value")
		}
		if part == "*" {
			continue
		}
		if strings.Contains(part, "/") {
			parts := strings.SplitN(part, "/", 2)
			step, err := strconv.Atoi(parts[1])
			if err != nil || step <= 0 {
				return fmt.Errorf("invalid step %q", parts[1])
			}
			if parts[0] != "*" {
				if err := validateRange(parts[0], min, max); err != nil {
					return err
				}
			}
			continue
		}
		if strings.Contains(part, "-") {
			if err := validateRange(part, min, max); err != nil {
				return err
			}
			continue
		}
		val, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("invalid value %q", part)
		}
		if val < min || val > max {
			return fmt.Errorf("value %d out of range %d-%d", val, min, max)
		}
	}
	return nil
}

func validateRange(expr string, min, max int) error {
	parts := strings.SplitN(expr, "-", 2)
	if len(parts) != 2 {
		val, err := strconv.Atoi(expr)
		if err != nil {
			return fmt.Errorf("invalid value %q", expr)
		}
		if val < min || val > max {
			return fmt.Errorf("value %d out of range %d-%d", val, min, max)
		}
		return nil
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("invalid range start %q", parts[0])
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("invalid range end %q", parts[1])
	}
	if start < min || end > max || start > end {
		return fmt.Errorf("range %d-%d invalid (must be within %d-%d)", start, end, min, max)
	}
	return nil
}

// NextTime computes the next time a cron expression fires after `after`.
// Returns after + 1 hour if the expression is invalid or no match is found
// within 366 days.
func NextTime(expr string, after time.Time) time.Time {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return after.Add(1 * time.Hour)
	}

	minuteSet := parseField(fields[0], 0, 59)
	hourSet := parseField(fields[1], 0, 23)
	domSet := parseField(fields[2], 1, 31)
	monthSet := parseField(fields[3], 1, 12)
	dowSet := parseField(fields[4], 0, 6)

	// Start scanning from the next minute after `after`.
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.Add(366 * 24 * time.Hour)

	for candidate.Before(limit) {
		if monthSet[int(candidate.Month())] &&
			domSet[candidate.Day()] &&
			dowSet[int(candidate.Weekday())] &&
			hourSet[candidate.Hour()] &&
			minuteSet[candidate.Minute()] {
			return candidate
		}

		// Advance efficiently: skip to next valid minute/hour/day.
		if !monthSet[int(candidate.Month())] {
			candidate = time.Date(candidate.Year(), candidate.Month()+1, 1, 0, 0, 0, 0, candidate.Location())
			continue
		}
		if !domSet[candidate.Day()] || !dowSet[int(candidate.Weekday())] {
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day()+1, 0, 0, 0, 0, candidate.Location())
			continue
		}
		if !hourSet[candidate.Hour()] {
			candidate = candidate.Truncate(time.Hour).Add(time.Hour)
			continue
		}
		candidate = candidate.Add(time.Minute)
	}

	return after.Add(1 * time.Hour)
}

// parseField parses a single cron field and returns a set of valid values.
func parseField(field string, min, max int) map[int]bool {
	set := make(map[int]bool)

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)

		// Handle */N step syntax.
		if strings.Contains(part, "/") {
			parts := strings.SplitN(part, "/", 2)
			step, err := strconv.Atoi(parts[1])
			if err != nil || step <= 0 {
				step = 1
			}
			rangeStart := min
			rangeEnd := max
			if parts[0] != "*" {
				if rangeParts := strings.SplitN(parts[0], "-", 2); len(rangeParts) == 2 {
					var err1, err2 error
					rangeStart, err1 = strconv.Atoi(rangeParts[0])
					rangeEnd, err2 = strconv.Atoi(rangeParts[1])
					if err1 != nil || err2 != nil {
						continue
					}
				} else {
					var err error
					rangeStart, err = strconv.Atoi(parts[0])
					if err != nil {
						continue
					}
				}
			}
			for i := rangeStart; i <= rangeEnd; i += step {
				set[i] = true
			}
			continue
		}

		// Handle * (all values).
		if part == "*" {
			for i := min; i <= max; i++ {
				set[i] = true
			}
			continue
		}

		// Handle range (1-5).
		if strings.Contains(part, "-") {
			rangeParts := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(rangeParts[0])
			end, err2 := strconv.Atoi(rangeParts[1])
			if err1 != nil || err2 != nil {
				continue
			}
			for i := start; i <= end; i++ {
				set[i] = true
			}
			continue
		}

		// Handle single value.
		val, err := strconv.Atoi(part)
		if err == nil && val >= min && val <= max {
			set[val] = true
		}
	}

	// If empty (parse error), default to all values.
	if len(set) == 0 {
		for i := min; i <= max; i++ {
			set[i] = true
		}
	}

	return set
}
