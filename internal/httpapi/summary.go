package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cpanato/tado-bridge/internal/history"
)

func summaryQuery(r *http.Request) (history.SummaryQuery, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return history.SummaryQuery{}, err
	}
	for key, items := range values {
		if len(items) != 1 {
			return history.SummaryQuery{}, errors.New("repeated summary parameter")
		}
		switch key {
		case "from", "to", "timezone", "max_gap", "home_id", "zone_id":
		default:
			return history.SummaryQuery{}, errors.New("unknown summary parameter")
		}
	}
	query := history.SummaryQuery{Timezone: "UTC", MaxGap: history.DefaultSummaryGap, HomeID: values.Get("home_id"), ZoneID: values.Get("zone_id")}
	query.From, err = time.Parse(time.RFC3339Nano, values.Get("from"))
	if err != nil {
		return history.SummaryQuery{}, err
	}
	query.To, err = time.Parse(time.RFC3339Nano, values.Get("to"))
	if err != nil {
		return history.SummaryQuery{}, err
	}
	if items, ok := values["timezone"]; ok {
		query.Timezone = items[0]
	}
	if items, ok := values["max_gap"]; ok {
		query.MaxGap, err = time.ParseDuration(items[0])
		if err != nil {
			return history.SummaryQuery{}, err
		}
	}
	return query, query.Validate()
}

func summaryHandler(reader history.Summarizer, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !get(w, r) || !authenticate(w, r, token) {
			return
		}
		query, err := summaryQuery(r)
		if err != nil {
			failure(w, http.StatusBadRequest, "invalid_summary_query", "Use RFC3339 from/to (at most 31 days), an IANA timezone, max_gap between 1m and 24h, and optional home_id/zone_id.")
			return
		}
		result, err := reader.Summarize(r.Context(), query)
		if err != nil {
			failure(w, http.StatusServiceUnavailable, "history_unavailable", "Historical summaries are temporarily unavailable; select a smaller window or a room if needed.")
			return
		}
		respond(w, http.StatusOK, result)
	}
}
