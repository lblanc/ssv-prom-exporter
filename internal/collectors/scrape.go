package collectors

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Child is what inventory/health/perf implement instead of
// prometheus.Collector directly. The wrapper Scrape collector takes
// care of emitting the shared scrape-success and scrape-duration
// metrics around each child's CollectMetrics call.
type Child interface {
	Name() string
	Describe(ch chan<- *prometheus.Desc)
	CollectMetrics(ctx context.Context, ch chan<- prometheus.Metric) (ok bool)
}

// Scrape is the prometheus.Collector that the registry actually sees.
// It runs each child concurrently on every scrape, tracking each one's
// success and duration as ssv_up{collector="..."} and
// ssv_scrape_duration_seconds{collector="..."}.
type Scrape struct {
	log      *slog.Logger
	timeout  time.Duration
	children []Child

	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc
}

func NewScrape(log *slog.Logger, timeout time.Duration, children ...Child) *Scrape {
	if log == nil {
		log = slog.Default()
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Scrape{
		log:            log,
		timeout:        timeout,
		children:       children,
		up:             desc("up", "1 if the last SSV scrape succeeded, 0 otherwise.", []string{"collector"}),
		scrapeDuration: desc("scrape_duration_seconds", "Duration of the last SSV scrape, per collector.", []string{"collector"}),
	}
}

func (s *Scrape) Describe(ch chan<- *prometheus.Desc) {
	ch <- s.up
	ch <- s.scrapeDuration
	for _, c := range s.children {
		c.Describe(ch)
	}
}

func (s *Scrape) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, c := range s.children {
		wg.Add(1)
		go func(c Child) {
			defer wg.Done()
			start := time.Now()
			ok := c.CollectMetrics(ctx, ch)
			ch <- prometheus.MustNewConstMetric(s.up, prometheus.GaugeValue, btof(ok), c.Name())
			ch <- prometheus.MustNewConstMetric(s.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds(), c.Name())
		}(c)
	}
	wg.Wait()
}
