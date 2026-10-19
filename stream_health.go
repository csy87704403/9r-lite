package main

import (
	"sort"
	"time"
)

const streamHealthRetention = 7 * 24 * time.Hour

// Sparse minute buckets bound history without storing successful response bodies.
type StreamHealthBucket struct {
	Minute        int64 `json:"minute"`
	Calls         int64 `json:"calls"`
	Interruptions int64 `json:"interruptions,omitempty"`
}

type StreamHealthStats struct {
	ProviderID        string               `json:"provider_id"`
	ProviderName      string               `json:"provider_name"`
	Model             string               `json:"model"`
	Buckets           []StreamHealthBucket `json:"buckets"`
	LastInterruption  string               `json:"last_interruption,omitempty"`
	LastInterruptedAt int64                `json:"last_interrupted_at,omitempty"`
}

type StreamHealthRank struct {
	ProviderID        string  `json:"provider_id"`
	ProviderName      string  `json:"provider_name"`
	Model             string  `json:"model"`
	Calls             int64   `json:"calls"`
	Interruptions     int64   `json:"interruptions"`
	InterruptionRate  float64 `json:"interruption_rate"`
	LastInterruption  string  `json:"last_interruption"`
	LastInterruptedAt int64   `json:"last_interrupted_at"`
}

func (w *usageResponseWriter) streamInterrupted() bool {
	// Initial error events (e.g. 429/402 in HTTP 200) are rejections, not lost streams.
	if len(w.inspection.failure) > 0 {
		return w.inspection.output
	}
	// A finish_reason is sufficient: missing [DONE] or a late socket close after
	// semantic completion must not be counted as a model interruption.
	return !w.inspection.complete
}

func (s *Server) recordStreamHealth(p ProviderConfig, model string, interrupted bool, reason string, now time.Time) {
	s.usageMu.Lock()
	if s.usage.StreamHealth == nil {
		s.usage.StreamHealth = map[string]*StreamHealthStats{}
	}
	key := p.ID + "/" + model
	stats := s.usage.StreamHealth[key]
	if stats == nil {
		stats = &StreamHealthStats{ProviderID: p.ID, Model: model}
		s.usage.StreamHealth[key] = stats
	}
	stats.ProviderName = p.Name
	minute := now.Truncate(time.Minute).Unix()
	i := sort.Search(len(stats.Buckets), func(i int) bool { return stats.Buckets[i].Minute >= minute })
	if i == len(stats.Buckets) || stats.Buckets[i].Minute != minute {
		stats.Buckets = append(stats.Buckets, StreamHealthBucket{})
		copy(stats.Buckets[i+1:], stats.Buckets[i:])
		stats.Buckets[i] = StreamHealthBucket{Minute: minute}
	}
	stats.Buckets[i].Calls++
	if interrupted {
		stats.Buckets[i].Interruptions++
		if now.Unix() >= stats.LastInterruptedAt {
			stats.LastInterruptedAt = now.Unix()
			stats.LastInterruption = truncateString(reason, 2000)
		}
	}
	pruneStreamHealthBuckets(stats, now)
	s.usage.UpdatedAt = now.Unix()
	s.usageMu.Unlock()
	s.scheduleUsageSave()
}

func pruneStreamHealthBuckets(stats *StreamHealthStats, now time.Time) {
	cutoff := now.Add(-streamHealthRetention).Truncate(time.Minute).Unix()
	i := sort.Search(len(stats.Buckets), func(i int) bool { return stats.Buckets[i].Minute >= cutoff })
	if i > 0 {
		copy(stats.Buckets, stats.Buckets[i:])
		stats.Buckets = stats.Buckets[:len(stats.Buckets)-i]
	}
}

func pruneStreamHealth(store *UsageStore, now time.Time) {
	for key, stats := range store.StreamHealth {
		if stats == nil {
			delete(store.StreamHealth, key)
			continue
		}
		pruneStreamHealthBuckets(stats, now)
		if len(stats.Buckets) == 0 {
			delete(store.StreamHealth, key)
		} else if stats.LastInterruptedAt < now.Add(-streamHealthRetention).Truncate(time.Minute).Unix() {
			stats.LastInterruption, stats.LastInterruptedAt = "", 0
		}
	}
}

func streamHealthRanking(store UsageStore, hours int, now time.Time) []StreamHealthRank {
	rows := make([]StreamHealthRank, 0)
	cutoff := now.Add(-time.Duration(hours) * time.Hour).Truncate(time.Minute).Unix()
	for _, stats := range store.StreamHealth {
		if stats == nil {
			continue
		}
		row := StreamHealthRank{ProviderID: stats.ProviderID, ProviderName: stats.ProviderName, Model: stats.Model,
			LastInterruption: stats.LastInterruption, LastInterruptedAt: stats.LastInterruptedAt}
		for _, bucket := range stats.Buckets {
			if bucket.Minute >= cutoff && bucket.Minute <= now.Unix() {
				row.Calls += bucket.Calls
				row.Interruptions += bucket.Interruptions
			}
		}
		if row.Interruptions == 0 {
			continue
		}
		row.InterruptionRate = float64(row.Interruptions) / float64(row.Calls)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Interruptions != rows[j].Interruptions {
			return rows[i].Interruptions > rows[j].Interruptions
		}
		if rows[i].LastInterruptedAt != rows[j].LastInterruptedAt {
			return rows[i].LastInterruptedAt > rows[j].LastInterruptedAt
		}
		return rows[i].ProviderID+"/"+rows[i].Model < rows[j].ProviderID+"/"+rows[j].Model
	})
	return rows
}
