package main

import (
	"slices"
	"sync"
	"time"
)

// latencyRecorder collects delivery-latency samples (server persist time ->
// this connection observing the frame). Fine at load-test scale; not meant
// for millions of samples.
type latencyRecorder struct {
	mu      sync.Mutex
	samples []time.Duration
}

func newLatencyRecorder() *latencyRecorder {
	return &latencyRecorder{}
}

func (r *latencyRecorder) record(d time.Duration) {
	r.mu.Lock()
	r.samples = append(r.samples, d)
	r.mu.Unlock()
}

type latencyReport struct {
	Count int
	P50   time.Duration
	P90   time.Duration
	P99   time.Duration
	Max   time.Duration
}

func (r *latencyRecorder) report() latencyReport {
	r.mu.Lock()
	samples := append([]time.Duration(nil), r.samples...)
	r.mu.Unlock()

	if len(samples) == 0 {
		return latencyReport{}
	}
	slices.Sort(samples)
	pick := func(p float64) time.Duration {
		idx := int(p * float64(len(samples)-1))
		return samples[idx]
	}
	return latencyReport{
		Count: len(samples),
		P50:   pick(0.50),
		P90:   pick(0.90),
		P99:   pick(0.99),
		Max:   samples[len(samples)-1],
	}
}
