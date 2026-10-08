package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

const sampledFirings = 100

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func parse(expr string) (cron.Schedule, error) {
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule is not a 5-field cron expression: %w", err)
	}
	return s, nil
}

func Validate(expr string) error {
	_, err := parse(expr)
	return err
}

func Next(expr string, after time.Time) (time.Time, error) {
	s, err := parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after.UTC()), nil
}

func MinInterval(expr string, from time.Time) (time.Duration, error) {
	s, err := parse(expr)
	if err != nil {
		return 0, err
	}
	previous := s.Next(from.UTC())
	smallest := time.Duration(1<<63 - 1)
	for range sampledFirings {
		if previous.IsZero() {
			break
		}
		next := s.Next(previous)
		if next.IsZero() {
			break
		}
		if gap := next.Sub(previous); gap < smallest {
			smallest = gap
		}
		previous = next
	}
	return smallest, nil
}

func Due(expr string, last, now time.Time) (bool, error) {
	if last.IsZero() {
		return true, Validate(expr)
	}
	if last.After(now) {
		return false, Validate(expr)
	}
	next, err := Next(expr, last)
	if err != nil {
		return false, err
	}
	return !next.After(now), nil
}
