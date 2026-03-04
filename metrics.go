package caddyscope

import (
	"cmp"
	"math"
	"slices"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// HostStats holds aggregated metrics for a single virtual host.
type HostStats struct {
	Host          string             `json:"host"`
	TotalRequests float64            `json:"total_requests"`
	StatusCodes   map[string]float64 `json:"status_codes"`
	DurationP50   float64            `json:"duration_p50"`
	DurationP95   float64            `json:"duration_p95"`
	DurationP99   float64            `json:"duration_p99"`
}

// metricsGatherer is a function that returns metric families.
// This matches the signature of prometheus.Gatherer.Gather() and allows
// easy testing without importing prometheus client directly.
type metricsGatherer func() ([]*dto.MetricFamily, error)

const (
	requestsTotalMetric   = "caddy_http_requests_total"
	requestDurationMetric = "caddy_http_request_duration_seconds"
)

// hostData accumulates raw metric values for one host.
type hostData struct {
	totalRequests   float64
	statusCodes     map[string]float64
	histogram       []*dto.Bucket
	hasCounterCodes bool // true if caddy_http_requests_total had code labels
}

// hostAccumulator collects hostData by host name.
type hostAccumulator map[string]*hostData

// gatherStats reads Prometheus metrics and aggregates per-host statistics.
func gatherStats(gather metricsGatherer) []HostStats {
	families, err := gather()
	if err != nil {
		return nil
	}

	acc := hostAccumulator{}
	// Counters first, so the histogram pass knows whether codes are already counted.
	acc.addRequestTotals(familyByName(families, requestsTotalMetric))
	acc.addDurations(familyByName(families, requestDurationMetric))
	return acc.stats()
}

// familyByName returns the metric family with the given name, or nil.
func familyByName(families []*dto.MetricFamily, name string) *dto.MetricFamily {
	for _, mf := range families {
		if mf.GetName() == name {
			return mf
		}
	}
	return nil
}

func (a hostAccumulator) host(name string) *hostData {
	if hd, ok := a[name]; ok {
		return hd
	}
	hd := &hostData{statusCodes: make(map[string]float64)}
	a[name] = hd
	return hd
}

// addRequestTotals adds request counters. Protobuf getters are nil-safe, so a
// missing family is a no-op.
func (a hostAccumulator) addRequestTotals(mf *dto.MetricFamily) {
	for _, m := range mf.GetMetric() {
		host := labelValue(m, "host")
		if host == "" {
			continue
		}
		hd := a.host(host)
		val := m.GetCounter().GetValue()
		hd.totalRequests += val

		if code := labelValue(m, "code"); code != "" {
			hd.statusCodes[code] += val
			hd.hasCounterCodes = true
		}
	}
}

// addDurations adds histogram buckets and, as a fallback, status codes from
// the histogram's code label.
func (a hostAccumulator) addDurations(mf *dto.MetricFamily) {
	for _, m := range mf.GetMetric() {
		host := labelValue(m, "host")
		h := m.GetHistogram()
		if host == "" || h == nil {
			continue
		}
		hd := a.host(host)
		hd.histogram = append(hd.histogram, h.GetBucket()...)
		hd.addFallbackCode(labelValue(m, "code"), float64(h.GetSampleCount()))
	}
}

// addFallbackCode counts a status code from the histogram only when
// caddy_http_requests_total lacks code labels.
func (hd *hostData) addFallbackCode(code string, count float64) {
	if hd.hasCounterCodes || code == "" {
		return
	}
	hd.statusCodes[code] += count
}

// stats converts the accumulated data into HostStats sorted by host.
func (a hostAccumulator) stats() []HostStats {
	if len(a) == 0 {
		return nil
	}

	stats := make([]HostStats, 0, len(a))
	for host, hd := range a {
		stats = append(stats, HostStats{
			Host:          host,
			TotalRequests: hd.totalRequests,
			StatusCodes:   hd.statusCodes,
			DurationP50:   histogramQuantile(hd.histogram, 0.50),
			DurationP95:   histogramQuantile(hd.histogram, 0.95),
			DurationP99:   histogramQuantile(hd.histogram, 0.99),
		})
	}

	slices.SortFunc(stats, func(x, y HostStats) int {
		return strings.Compare(x.Host, y.Host)
	})
	return stats
}

// labelValue returns the value of the named label from a metric, or "".
func labelValue(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// bucket is a merged histogram bucket.
type bucket struct {
	upperBound      float64
	cumulativeCount float64
}

// histogramQuantile computes an approximated quantile from histogram buckets
// using the same linear interpolation algorithm as Prometheus histogram_quantile().
func histogramQuantile(buckets []*dto.Bucket, q float64) float64 {
	sorted := mergeBuckets(buckets)
	if len(sorted) == 0 {
		return 0
	}

	// Total count is from the +Inf bucket (last one after sorting).
	total := sorted[len(sorted)-1].cumulativeCount
	if total == 0 {
		return 0
	}

	rank := q * total
	for i, b := range sorted {
		if b.cumulativeCount >= rank {
			return interpolate(sorted, i, rank)
		}
	}
	return sorted[len(sorted)-1].upperBound
}

// mergeBuckets sums counts of buckets that share an upper bound and returns
// them sorted by upper bound.
func mergeBuckets(buckets []*dto.Bucket) []bucket {
	merged := make(map[float64]float64, len(buckets))
	for _, b := range buckets {
		merged[b.GetUpperBound()] += float64(b.GetCumulativeCount())
	}

	sorted := make([]bucket, 0, len(merged))
	for ub, count := range merged {
		sorted = append(sorted, bucket{upperBound: ub, cumulativeCount: count})
	}
	slices.SortFunc(sorted, func(x, y bucket) int {
		return cmp.Compare(x.upperBound, y.upperBound)
	})
	return sorted
}

// interpolate returns the value at rank inside bucket i, assuming samples are
// spread evenly between the bucket's lower and upper bound.
func interpolate(sorted []bucket, i int, rank float64) float64 {
	var lower bucket
	if i > 0 {
		lower = sorted[i-1]
	}

	b := sorted[i]
	if math.IsInf(b.upperBound, 1) {
		return lower.upperBound
	}

	bucketCount := b.cumulativeCount - lower.cumulativeCount
	if bucketCount == 0 {
		return lower.upperBound
	}

	rankInBucket := rank - lower.cumulativeCount
	return lower.upperBound + (b.upperBound-lower.upperBound)*(rankInBucket/bucketCount)
}
