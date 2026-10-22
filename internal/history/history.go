// Package history persists completed snapshots without owning an upstream client.
package history

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

const (
	DefaultRetention = 90 * 24 * time.Hour
	MaxLimit         = 100
	MaxRange         = 31 * 24 * time.Hour
	maxPageBytes     = 8 << 20
)

// Query selects [From, To), newest first. To is also the pagination cursor.
type Query struct {
	From  time.Time
	To    time.Time
	Limit int
}

func (q Query) Validate() error {
	if q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To) || q.To.Sub(q.From) > MaxRange || q.Limit < 1 || q.Limit > MaxLimit {
		return errors.New("history requires from < to, a range of at most 31 days and limit between 1 and 100")
	}
	return nil
}

type Page struct {
	Snapshots []json.RawMessage `json:"snapshots"`
	NextTo    *time.Time        `json:"next_to,omitempty"`
}

type Reader interface {
	Query(context.Context, Query) (Page, error)
}

// ValidateURL requires an explicit destination and TLS policy. Never return the
// supplied value: connection URLs may contain passwords and private hostnames.
func ValidateURL(value string) error {
	u, err := url.Parse(value)
	if err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Hostname() != "" && len(u.Path) > 1 && u.Fragment == "" {
		params, parseErr := url.ParseQuery(u.RawQuery)
		if parseErr == nil && len(params["sslmode"]) == 1 {
			switch params.Get("sslmode") {
			case "verify-full", "verify-ca", "require", "disable":
				return nil
			}
		}
	}
	return errors.New("HISTORY_DATABASE_URL must be a PostgreSQL URL with a host, database and explicit sslmode (verify-full, verify-ca, require or disable)")
}
