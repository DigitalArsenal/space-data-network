package format4proof

import (
	"math"
	"sort"
)

// Percentile is the nearest-rank q-quantile of v (q in [0, 1]): the value at
// rank ceil(q*n), so p99 of 64 samples is the largest. NaN for no samples.
// It is the rule every earlier baseline (zzbaseline, bar.py) used, so the
// numbers stay comparable.
func Percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

// Median is the middle sample (the mean of the middle two for an even count),
// NaN for none.
func Median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Point is one measurement of a metric at a store size.
type Point struct {
	Records float64 `json:"records"`
	Value   float64 `json:"value"`
}

// PerDoubling is the factor a metric grows by per doubling of the store: the
// least-squares slope b of log2(value) against log2(records), returned as
// 2^b. Two points give (v1/v0)^(1/log2(n1/n0)) exactly. ok is false when
// fewer than two points have positive values and distinct record counts.
func PerDoubling(points []Point) (factor float64, ok bool) {
	var xs, ys []float64
	for _, p := range points {
		if p.Records <= 0 || p.Value <= 0 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		xs = append(xs, math.Log2(p.Records))
		ys = append(ys, math.Log2(p.Value))
	}
	if len(xs) < 2 {
		return math.NaN(), false
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(ys))
	var sxy, sxx float64
	for i := range xs {
		sxy += (xs[i] - mx) * (ys[i] - my)
		sxx += (xs[i] - mx) * (xs[i] - mx)
	}
	if sxx == 0 {
		return math.NaN(), false
	}
	return math.Exp2(sxy / sxx), true
}
