// Package metrics exports bounded climate aggregates without identifying labels.
package metrics

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/cpanato/tado-bridge/internal/domain"
	"github.com/cpanato/tado-bridge/internal/store"
)

type Exporter struct {
	registry    *prometheus.Registry
	errors      prometheus.Counter
	collections *prometheus.CounterVec
	requests    *prometheus.CounterVec
}

func New(cache *store.Store, now func() time.Time) *Exporter {
	if now == nil {
		now = time.Now
	}
	exporter := &Exporter{
		registry:    prometheus.NewRegistry(),
		errors:      prometheus.NewCounter(prometheus.CounterOpts{Name: "tado_bridge_collection_errors_total", Help: "Failed collection attempts, including cache replacement failures."}),
		collections: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tado_bridge_collections_total", Help: "Collection attempts by result."}, []string{"result"}),
		requests:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tado_bridge_upstream_requests_total", Help: "Attempted upstream HTTP requests by request class."}, []string{"kind"}),
	}
	// Initialize the fixed label values, including before the first collection.
	for _, result := range []string{"success", "error"} {
		exporter.collections.WithLabelValues(result)
	}
	for _, kind := range []string{"api", "oauth"} {
		exporter.requests.WithLabelValues(kind)
	}
	exporter.registry.MustRegister(exporter.errors, exporter.collections, exporter.requests, &snapshotCollector{
		cache: cache, now: now,
		last:        prometheus.NewDesc("tado_bridge_last_successful_collection_timestamp_seconds", "Unix timestamp of the cached successful collection.", nil, nil),
		age:         prometheus.NewDesc("tado_bridge_snapshot_age_seconds", "Age of the cached collection; this does not measure sensor freshness.", nil, nil),
		temperature: prometheus.NewDesc("tado_bridge_temperature_celsius", "Aggregate available measured temperatures across all cached rooms.", []string{"statistic"}, nil),
		target:      prometheus.NewDesc("tado_bridge_target_temperature_celsius", "Aggregate available active target temperatures across all cached rooms.", []string{"statistic"}, nil),
		humidity:    prometheus.NewDesc("tado_bridge_humidity_percent", "Aggregate available humidity across all cached rooms.", []string{"statistic"}, nil),
	})
	return exporter
}

func (e *Exporter) ObserveCollection(err error) {
	result := "success"
	if err != nil {
		result = "error"
		e.errors.Inc()
	}
	e.collections.WithLabelValues(result).Inc()
}

func (e *Exporter) ObserveRequests(api, oauth int) {
	e.requests.WithLabelValues("api").Add(float64(api))
	e.requests.WithLabelValues("oauth").Add(float64(oauth))
}

func (e *Exporter) Handler() http.Handler {
	return promhttp.HandlerFor(e.registry, promhttp.HandlerOpts{DisableCompression: true})
}

type snapshotCollector struct {
	cache                                    *store.Store
	now                                      func() time.Time
	last, age, temperature, target, humidity *prometheus.Desc
}

func (c *snapshotCollector) Describe(output chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.last, c.age, c.temperature, c.target, c.humidity} {
		output <- desc
	}
}

func (c *snapshotCollector) Collect(output chan<- prometheus.Metric) {
	view, ok := c.cache.Load()
	if !ok {
		return
	}
	output <- prometheus.MustNewConstMetric(c.last, prometheus.GaugeValue, float64(view.CollectedAt.UnixNano())/float64(time.Second))
	output <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue, max(0, c.now().Sub(view.CollectedAt).Seconds()))
	var snapshot domain.Snapshot
	if json.Unmarshal(view.JSON, &snapshot) != nil {
		return
	}
	var temperature, target, humidity aggregate
	for _, home := range snapshot.Homes {
		for _, zone := range home.Zones {
			temperature.add(zone.TemperatureC)
			target.add(zone.TargetTemperatureC)
			humidity.add(zone.HumidityPercent)
		}
	}
	temperature.emit(output, c.temperature)
	target.emit(output, c.target)
	humidity.emit(output, c.humidity)
}

type aggregate struct {
	count         int
	sum, min, max float64
}

func (a *aggregate) add(value *float64) {
	if value == nil {
		return
	}
	if a.count == 0 {
		a.min = *value
		a.max = *value
	}
	a.count++
	a.sum += *value
	a.min = min(a.min, *value)
	a.max = max(a.max, *value)
}
func (a aggregate) emit(output chan<- prometheus.Metric, desc *prometheus.Desc) {
	if a.count == 0 {
		return
	}
	output <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, a.min, "min")
	output <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, a.max, "max")
	output <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, a.sum/float64(a.count), "mean")
}
