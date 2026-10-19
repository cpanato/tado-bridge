package tado

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func retryDeadline(header http.Header, now time.Time) time.Time {
	var deadline time.Time
	if raw := header.Get("Retry-After"); raw != "" {
		if seconds, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
			deadline = addSeconds(now, seconds)
		} else if at, err := http.ParseTime(raw); err == nil && at.After(now) {
			deadline = at
		}
	}
	// tado also reports an exhausted daily quota as: "perday";r=0;t=123.
	exhausted := false
	var reset string
	for _, part := range strings.Split(header.Get("RateLimit"), ";") {
		part = strings.TrimSpace(part)
		if part == "r=0" {
			exhausted = true
		}
		if value, ok := strings.CutPrefix(part, "t="); ok {
			reset = value
		}
	}
	if exhausted {
		if seconds, err := strconv.ParseUint(reset, 10, 64); err == nil {
			at := addSeconds(now, seconds)
			if at.After(deadline) {
				deadline = at
			}
		}
	}
	return deadline
}

func addSeconds(now time.Time, seconds uint64) time.Time {
	if seconds > uint64(math.MaxInt64/int64(time.Second)) {
		return now.Add(time.Duration(math.MaxInt64))
	}
	return now.Add(time.Duration(seconds) * time.Second)
}
