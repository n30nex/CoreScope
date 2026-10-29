package main

import (
	"context"
	"math"
	"sort"
	"time"
)

// The per-observer decoder is shared with ordinary observer metrics. Keep the
// raw counter numerator and elapsed-time denominator for weighted source means;
// averaging displayed (rounded) percentages would bias sparse reporters.
func buildCrossBorderDashboardRadio(ctx context.Context, snapshot crossBorderSnapshot, since int64, now time.Time, limit int) (CrossBorderRadio, error) {
	result := CrossBorderRadio{Status: "unavailable", Truncated: snapshot.DashboardMetricsTruncated, Observers: []CrossBorderRadioObserver{}}
	if !snapshot.DashboardMetricsAvailable {
		return result, nil
	}
	result.Status = "empty"
	type point struct {
		sample rawMetricsSample
		at     time.Time
	}
	type series struct {
		observer CrossBorderObserver
		points   []point
	}
	groups := map[string]*series{}
	cutoff := crossBorderMetricsCutoff(now)
	for i, row := range snapshot.DashboardMetrics {
		if i&127 == 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
		if i >= crossBorderDashboardMetrics {
			result.Truncated = true
			break
		}
		at, err := time.Parse(time.RFC3339, row.Sample.Timestamp)
		if err != nil {
			result.ExcludedIntervals++
			continue
		}
		if at.Unix() < since || at.After(cutoff) {
			continue
		}
		id := crossBorderID(row.Observer.ID)
		if id == "" {
			result.ExcludedIntervals++
			continue
		}
		group := groups[id]
		if group == nil {
			group = &series{observer: row.Observer}
			groups[id] = group
		}
		group.points = append(group.points, point{sample: row.Sample, at: at})
	}
	var totalTX, totalRX, secondsTX, secondsRX float64
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sort.SliceStable(group.points, func(i, j int) bool { return group.points[i].at.Before(group.points[j].at) })
		raw := make([]rawMetricsSample, len(group.points))
		for i, p := range group.points {
			raw[i] = p.sample
		}
		rates, _, err := computeDeltas(raw, crossBorderDashboardSampleSeconds)
		if err != nil {
			return result, err
		}
		observer := CrossBorderRadioObserver{ID: group.observer.ID, Name: group.observer.Name, IATA: group.observer.IATA, ReportedCanRelay: group.observer.ReportedCanRelay}
		last := group.points[len(group.points)-1].at.UTC().Format(time.RFC3339)
		observer.LatestAt = &last
		var tx, rx, txSeconds, rxSeconds float64
		for i := 1; i < len(group.points); i++ {
			cur, prev := group.points[i], group.points[i-1]
			seconds := cur.at.Sub(prev.at).Seconds()
			// computeDeltas handles missing/gapped/reset intervals, but its
			// legacy chart clamp must not turn impossible diagnostic data into100%.
			invalid := seconds <= 0
			pairs := [][2]*int{{prev.sample.TxAirSecs, cur.sample.TxAirSecs}, {prev.sample.RxAirSecs, cur.sample.RxAirSecs}, {prev.sample.RecvErrors, cur.sample.RecvErrors}, {prev.sample.PacketsSent, cur.sample.PacketsSent}, {prev.sample.PacketsRecv, cur.sample.PacketsRecv}}
			for _, pair := range pairs {
				if (pair[0] != nil && *pair[0] < 0) || (pair[1] != nil && *pair[1] < 0) {
					invalid = true
				}
			}
			for _, pair := range pairs[:2] {
				if pair[0] != nil && pair[1] != nil && seconds > 0 && (float64(*pair[1])-float64(*pair[0]))/seconds > 1 {
					invalid = true
				}
			}
			if invalid || rates[i].IsReboot || (rates[i].TxAirtimePct == nil && rates[i].RxAirtimePct == nil) {
				result.ExcludedIntervals++
				continue
			}
			observer.ValidSeconds += seconds
			if rates[i].TxAirtimePct != nil {
				tx += float64(*cur.sample.TxAirSecs) - float64(*prev.sample.TxAirSecs)
				txSeconds += seconds
			}
			if rates[i].RxAirtimePct != nil {
				rx += float64(*cur.sample.RxAirSecs) - float64(*prev.sample.RxAirSecs)
				rxSeconds += seconds
			}
		}
		percentage := func(value, seconds float64) *float64 {
			if seconds <= 0 {
				return nil
			}
			pct := math.Round(value/seconds*10000) / 100
			return &pct
		}
		observer.TxAirtimePct = percentage(tx, txSeconds)
		observer.RxAirtimePct = percentage(rx, rxSeconds)
		if observer.TxAirtimePct != nil || observer.RxAirtimePct != nil {
			result.ReportingObservers++
		}
		totalTX += tx
		totalRX += rx
		secondsTX += txSeconds
		secondsRX += rxSeconds
		result.Observers = append(result.Observers, observer)
	}
	if secondsTX > 0 {
		value := math.Round(totalTX/secondsTX*10000) / 100
		result.TxAirtimePct = &value
	}
	if secondsRX > 0 {
		value := math.Round(totalRX/secondsRX*10000) / 100
		result.RxAirtimePct = &value
	}
	if result.ReportingObservers > 0 {
		result.Status = "available"
	}
	sort.Slice(result.Observers, func(i, j int) bool {
		a, b := result.Observers[i], result.Observers[j]
		load := func(o CrossBorderRadioObserver) float64 {
			v := -1.0
			if o.TxAirtimePct != nil {
				v = *o.TxAirtimePct
			}
			return v
		}
		if load(a) != load(b) {
			return load(a) > load(b)
		}
		return a.ID < b.ID
	})
	if len(result.Observers) > limit {
		result.Observers = append([]CrossBorderRadioObserver{}, result.Observers[:limit]...)
		result.Truncated = true
	}
	return result, ctx.Err()
}
