package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	reg     prometheus.Registerer
	metric  *prometheus.CounterVec
	updater chan MetricUpdate
}

type MetricUpdate struct {
	Reason string
	Value  float64
}

func (m *Metrics) start() {
	for u := range m.updater {
		c, err := m.metric.GetMetricWith(prometheus.Labels{"reason": u.Reason})
		if err != nil {
			log.Printf("Error getting a label with reason %s: %s", u.Reason, err)
			continue
		}
		c.Add(u.Value)
	}
}

func addMetrics(port int) chan<- MetricUpdate {
	m := Metrics{reg: prometheus.NewRegistry(), metric: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "restarts"}, []string{"reason"}), updater: make(chan MetricUpdate)}
	m.reg.MustRegister(m.metric)
	http.Handle("/metrics", promhttp.HandlerFor(m.reg.(prometheus.Gatherer), promhttp.HandlerOpts{Registry: m.reg}))
	if port > 0 {
		go http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil)
	}
	go m.start()
	return m.updater
}
