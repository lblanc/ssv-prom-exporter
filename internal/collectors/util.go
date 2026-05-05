package collectors

import "github.com/prometheus/client_golang/prometheus"

const namespace = "ssv"

func desc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

func btof(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
