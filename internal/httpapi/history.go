package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
)

func historyQuery(r *http.Request) (history.Query, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return history.Query{}, errors.New("invalid query")
	}
	for key, items := range values {
		if (key != "from" && key != "to" && key != "limit") || len(items) != 1 {
			return history.Query{}, errors.New("unknown or repeated history parameter")
		}
	}
	from, err := time.Parse(time.RFC3339Nano, values.Get("from"))
	if err != nil {
		return history.Query{}, errors.New("invalid from")
	}
	to, err := time.Parse(time.RFC3339Nano, values.Get("to"))
	if err != nil {
		return history.Query{}, errors.New("invalid to")
	}
	limit := 100
	if items, ok := values["limit"]; ok {
		limit, err = strconv.Atoi(items[0])
		if err != nil {
			return history.Query{}, errors.New("invalid limit")
		}
	}
	query := history.Query{From: from, To: to, Limit: limit}
	return query, query.Validate()
}
