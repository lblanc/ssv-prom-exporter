package collectors

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

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

func itoa(i int) string { return strconv.Itoa(i) }

func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
