package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

const (
	autoStreamGuardWindow = 15 * time.Minute
	autoStreamIsolation   = 15 * time.Minute
)

type autoStreamGuardState struct {
	Epoch          uint64               `json:"epoch"`
	Consecutive    int                  `json:"consecutive,omitempty"`
	Until          int64                `json:"until,omitempty"`
	Reason         string               `json:"reason,omitempty"`
	LastObservedAt int64                `json:"last_observed_at,omitempty"`
	Window         []StreamHealthBucket `json:"window,omitempty"`
}

type autoStreamTrial struct {
	key          string
	epoch        uint64
	localTimeout atomic.Bool
}

func autoStreamTrialContext(ctx context.Context, trial *autoStreamTrial) context.Context {
	if trial == nil {
		return ctx
	}
	return withUpstreamTimeoutObserver(ctx, func() { trial.localTimeout.Store(true) })
}

type autoStreamHealthStatus struct {
	ProviderID        string `json:"provider_id"`
	Model             string `json:"model"`
	State             string `json:"state,omitempty"`
	Until             int64  `json:"until,omitempty"`
	Reason            string `json:"reason,omitempty"`
	Calls             int64  `json:"calls"`
	Interruptions     int64  `json:"interruptions"`
	LastInterruption  string `json:"last_interruption,omitempty"`
	LastInterruptedAt int64  `json:"last_interrupted_at,omitempty"`
}

func streamGuardCounts(buckets []StreamHealthBucket, now time.Time) (calls, interruptions int64) {
	cutoff := now.Add(-autoStreamGuardWindow).Truncate(time.Minute).Unix()
	for _, b := range buckets {
		if b.Minute >= cutoff && b.Minute <= now.Unix() {
			calls += b.Calls
			interruptions += b.Interruptions
		}
	}
	return
}

func openAutoStreamGuard(g *autoStreamGuardState, reason string, now time.Time) {
	g.Epoch++
	g.Until = now.Add(autoStreamIsolation).Unix()
	g.Reason = truncateString(reason, 2000)
}

// Called with usageMu held. Only new observations enter this guard; old ranking
// data from before timeout classification was fixed is never used to isolate.
func updateAutoStreamGuard(stats *StreamHealthStats, epoch uint64, interrupted, healthy bool, now time.Time) {
	if !interrupted && !healthy {
		return
	}
	if stats.Guard == nil {
		stats.Guard = &autoStreamGuardState{}
	}
	g := stats.Guard
	// An old in-flight completion cannot reset or reopen a newer isolation epoch.
	if epoch != g.Epoch || g.Until != 0 || now.Unix() < g.LastObservedAt {
		return
	}
	if g.LastObservedAt < now.Add(-autoStreamGuardWindow).Unix() {
		g.Consecutive = 0
	}
	g.LastObservedAt = now.Unix()
	cutoff := now.Add(-autoStreamGuardWindow).Truncate(time.Minute).Unix()
	window := g.Window[:0]
	for _, b := range g.Window {
		if b.Minute >= cutoff {
			window = append(window, b)
		}
	}
	g.Window = window
	minute := now.Truncate(time.Minute).Unix()
	if len(g.Window) == 0 || g.Window[len(g.Window)-1].Minute != minute {
		g.Window = append(g.Window, StreamHealthBucket{Minute: minute})
	}
	b := &g.Window[len(g.Window)-1]
	b.Calls++
	if interrupted {
		b.Interruptions++
		g.Consecutive++
	} else {
		g.Consecutive = 0
	}
	if !interrupted {
		return
	}
	calls, failures := streamGuardCounts(g.Window, now)
	if g.Consecutive >= 2 {
		openAutoStreamGuard(g, "连续 2 次真实断流，自动隔离 15 分钟", now)
	} else if calls >= 10 && failures*5 >= calls {
		openAutoStreamGuard(g, "近 15 分钟至少 10 次有效流式调用且断流率达到 20%，自动隔离 15 分钟", now)
	}
}

func (s *Server) autoStreamEpoch(providerID, model string) uint64 {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	if stats := s.usage.StreamHealth[providerID+"/"+model]; stats != nil && stats.Guard != nil {
		return stats.Guard.Epoch
	}
	return 0
}

func (s *Server) autoStreamBlocked(providerID, model string, now time.Time) bool {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	key := providerID + "/" + model
	if s.autoStreamTrials[key] != nil {
		return true
	}
	stats := s.usage.StreamHealth[key]
	return stats != nil && stats.Guard != nil && stats.Guard.Until > now.Unix()
}

// Admission must be checked again at dispatch, not just when building the list:
// several agents can receive the same candidate snapshot concurrently.
func (s *Server) admitAutoStreamCandidate(providerID, model string, stream bool, now time.Time) (*autoStreamTrial, bool) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	key := providerID + "/" + model
	stats := s.usage.StreamHealth[key]
	if s.autoStreamTrials[key] != nil {
		return nil, false
	}
	if stats == nil || stats.Guard == nil || stats.Guard.Until == 0 {
		return nil, true
	}
	g := stats.Guard
	if g.Until > now.Unix() || !stream {
		return nil, false
	}
	if s.autoStreamTrials == nil {
		s.autoStreamTrials = map[string]*autoStreamTrial{}
	}
	trial := &autoStreamTrial{key: key, epoch: g.Epoch}
	s.autoStreamTrials[key] = trial
	return trial, true
}

func (s *Server) finishAutoStreamTrial(trial *autoStreamTrial, w *autoRetryWriter, ctx context.Context, now time.Time) {
	if trial == nil {
		return
	}
	s.usageMu.Lock()
	if s.autoStreamTrials[trial.key] != trial {
		s.usageMu.Unlock()
		return
	}
	delete(s.autoStreamTrials, trial.key)
	stats := s.usage.StreamHealth[trial.key]
	if stats == nil || stats.Guard == nil || stats.Guard.Epoch != trial.epoch {
		s.usageMu.Unlock()
		return
	}
	// Cancellation, our own timeout, or downstream delivery failure is inconclusive.
	if w == nil || requestContextCanceled(ctx) || trial.localTimeout.Load() || errors.Is(w.readErr, errGatewayTimeout) || (w.writeErr != nil && !errors.Is(w.writeErr, errAutoStreamRejected) && !errors.Is(w.writeErr, errAutoStreamComplete)) {
		s.usageMu.Unlock()
		return
	}
	g := stats.Guard
	success := w.status >= 200 && w.status <= 299 && strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream") && w.stream.complete && w.stream.output && len(w.stream.failure) == 0 && w.readErr == nil
	if success {
		g.Epoch++
		g.Until, g.Reason, g.Consecutive = 0, "", 0
		g.Window = nil
		updateAutoStreamGuard(stats, g.Epoch, false, true, now)
	} else if w.status < 200 || w.status > 299 || len(w.stream.failure) > 0 || w.readErr != nil {
		body := w.stream.failure
		if len(body) == 0 {
			body = w.body.Bytes()
		}
		openAutoStreamGuard(g, "试恢复失败，再次隔离 15 分钟："+formatAutoRuntimeError(w.status, body), now)
	} // A JSON response to stream=true does not prove streaming recovered.
	s.usageMu.Unlock()
	s.scheduleUsageSave()
}

func (s *Server) autoStreamHealthSnapshot(providerID, model string, now time.Time) *autoStreamHealthStatus {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	key := providerID + "/" + model
	stats := s.usage.StreamHealth[key]
	if stats == nil || stats.Guard == nil {
		return nil
	}
	g := stats.Guard
	calls, failures := streamGuardCounts(g.Window, now)
	out := &autoStreamHealthStatus{ProviderID: providerID, Model: model, Calls: calls, Interruptions: failures, Until: g.Until, Reason: g.Reason,
		LastInterruption: stats.LastInterruption, LastInterruptedAt: stats.LastInterruptedAt}
	switch {
	case s.autoStreamTrials[key] != nil:
		out.State = "recovering"
	case g.Until > now.Unix():
		out.State = "isolated"
	case g.Until != 0:
		out.State = "awaiting_trial"
	case failures > 0:
		out.State = "recent"
	}
	return out
}

const autoStreamNoTarget = "auto 暂无可用目标：模型可能处于冷却、断流隔离或试恢复中"
