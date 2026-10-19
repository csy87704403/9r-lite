package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const usageFileName = "usage.json"

type UsageCounters struct {
	ClientRequests   int64 `json:"client_requests,omitempty"`
	ClientSuccess    int64 `json:"client_success,omitempty"`
	ClientFailed     int64 `json:"client_failed,omitempty"`
	UpstreamCalls    int64 `json:"upstream_calls,omitempty"`
	UpstreamSuccess  int64 `json:"upstream_success,omitempty"`
	UpstreamFailed   int64 `json:"upstream_failed,omitempty"`
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
	UnreportedCalls  int64 `json:"unreported_calls,omitempty"`
}

type UsageModelStats struct {
	ProviderID        string        `json:"provider_id"`
	ProviderName      string        `json:"provider_name"`
	Model             string        `json:"model"`
	Today             UsageCounters `json:"today"`
	Total             UsageCounters `json:"total"`
	LastFailure       string        `json:"last_failure,omitempty"`
	LastFailureAt     int64         `json:"last_failure_at,omitempty"`
	LastFailureStatus int           `json:"last_failure_status,omitempty"`
}

type UsageGroupStats struct {
	ID     string                      `json:"id"`
	Name   string                      `json:"name"`
	Today  UsageCounters               `json:"today"`
	Total  UsageCounters               `json:"total"`
	Models map[string]*UsageModelStats `json:"models,omitempty"`
}

type UsageStore struct {
	Version      int                           `json:"version"`
	Date         string                        `json:"date"`
	UpdatedAt    int64                         `json:"updated_at"`
	Groups       map[string]*UsageGroupStats   `json:"groups"`
	StreamHealth map[string]*StreamHealthStats `json:"stream_health,omitempty"`
}

type usageRequestInfo struct {
	GroupID   string
	GroupName string
}

type usageRequestContextKey struct{}

type tokenUsage struct {
	Prompt     int64
	Completion int64
	Total      int64
	Found      bool
}

type usageResponseWriter struct {
	target      http.ResponseWriter
	status      int
	stream      bool
	body        bytes.Buffer
	failureBody bytes.Buffer
	pending     []byte
	usage       tokenUsage
	inspection  sseInspection
	readErr     error
	writeErr    error
}

type usageClientWriter struct {
	target     http.ResponseWriter
	status     int
	inspection sseInspection
}

func newUsageStore() UsageStore {
	return UsageStore{Version: 1, Date: usageDate(), Groups: map[string]*UsageGroupStats{}}
}

func loadUsage(dataDir string) (UsageStore, error) {
	var store UsageStore
	err := loadJSONFile(path.Join(dataDir, usageFileName), func(b []byte) error {
		store = UsageStore{}
		return json.Unmarshal(b, &store)
	})
	if errors.Is(err, os.ErrNotExist) {
		return newUsageStore(), nil
	}
	if err != nil {
		if errors.Is(err, errDamagedJSONFile) {
			log.Printf("usage history damaged; recovery copy retained, starting fresh statistics")
			return newUsageStore(), nil
		}
		return UsageStore{}, err
	}
	if store.Groups == nil {
		store.Groups = map[string]*UsageGroupStats{}
	}
	if store.Version == 0 {
		store.Version = 1
	}
	rollUsageDate(&store)
	return store, nil
}

func usageDate() string {
	return time.Now().Format("2006-01-02")
}

func rollUsageDate(store *UsageStore) {
	today := usageDate()
	if store.Date == today {
		return
	}
	store.Date = today
	for _, group := range store.Groups {
		group.Today = UsageCounters{}
		for _, model := range group.Models {
			model.Today = UsageCounters{}
			model.LastFailure = ""
			model.LastFailureAt = 0
			model.LastFailureStatus = 0
		}
	}
}

func usageGroupForScope(scope accessScope) usageRequestInfo {
	if scope.Group != nil {
		return usageRequestInfo{GroupID: scope.Group.ID, GroupName: scope.Group.Name}
	}
	return usageRequestInfo{GroupID: "_master", GroupName: "主密钥"}
}

func usageInfoFromContext(ctx context.Context) *usageRequestInfo {
	info, _ := ctx.Value(usageRequestContextKey{}).(*usageRequestInfo)
	return info
}

func (s *Server) beginClientUsage(w http.ResponseWriter, r *http.Request, scope accessScope) (http.ResponseWriter, *http.Request, func()) {
	if bypass, _ := r.Context().Value(internalBypassKey{}).(bool); bypass || usageInfoFromContext(r.Context()) != nil {
		return w, r, func() {}
	}
	info := usageGroupForScope(scope)
	tracked := &usageClientWriter{target: w}
	r = r.WithContext(context.WithValue(r.Context(), usageRequestContextKey{}, &info))
	return tracked, r, func() {
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		if requestContextCanceled(r.Context()) {
			status = statusClientClosedRequest
		} else if strings.Contains(strings.ToLower(tracked.Header().Get("Content-Type")), "text/event-stream") {
			tracked.inspection.finish()
			if len(tracked.inspection.failure) > 0 || !tracked.inspection.complete {
				status = http.StatusBadGateway
			}
		}
		s.recordClientUsage(info, status)
	}
}

func (s *Server) beginUpstreamUsage(w http.ResponseWriter, r *http.Request, p ProviderConfig, model string, stream bool) (http.ResponseWriter, func()) {
	info := usageInfoFromContext(r.Context())
	if info == nil {
		return w, func() {}
	}
	tracked := &usageResponseWriter{target: w, stream: stream}
	return tracked, func() {
		tracked.finishStream()
		interrupted := tracked.streamInterrupted()
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		if requestContextCanceled(r.Context()) {
			return
		}
		usage := tracked.usage
		if !stream {
			usage = extractTokenUsage(tracked.body.Bytes())
		}
		responseBody := tracked.body.Bytes()
		if stream {
			responseBody = tracked.failureBody.Bytes()
		}
		usageStatus := status
		failureReason := ""
		if status < 200 || status > 299 {
			failureReason = usageFailureReason(status, responseBody)
		} else if tracked.readErr != nil {
			usageStatus = http.StatusBadGateway
			failureReason = "upstream response interrupted"
		} else if !stream && len(responseBody) < 8<<20 && !json.Valid(responseBody) {
			usageStatus = http.StatusBadGateway
			failureReason = "upstream returned incomplete or invalid JSON"
		} else if !stream && probeResponseHasExplicitError(responseBody) {
			usageStatus = http.StatusBadGateway
			failureReason = usageFailureReason(status, responseBody)
		} else if stream && strings.Contains(strings.ToLower(tracked.Header().Get("Content-Type")), "text/event-stream") {
			tracked.inspection.finish()
			if len(tracked.inspection.failure) == 0 && (!tracked.inspection.complete || !tracked.inspection.output) {
				tracked.inspection.fail("upstream stream ended without a complete model response")
			}
			if len(tracked.inspection.failure) > 0 {
				usageStatus = http.StatusBadGateway
				failureReason = usageFailureReason(status, tracked.inspection.failure)
			}
		}
		s.recordUpstreamUsage(*info, p, model, usageStatus, usage, failureReason, status)
		if stream && status >= 200 && status <= 299 && tracked.writeErr == nil && strings.Contains(strings.ToLower(tracked.Header().Get("Content-Type")), "text/event-stream") {
			if tracked.readErr != nil {
				failureReason = "上游流读取中断：" + truncateString(tracked.readErr.Error(), 1000)
			}
			s.recordStreamHealth(p, model, interrupted, failureReason, time.Now())
		}
	}
}

func (w *usageClientWriter) Header() http.Header { return w.target.Header() }

func (w *usageClientWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.target.WriteHeader(status)
}

func (w *usageClientWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		w.inspection.feed(body)
	}
	return w.target.Write(body)
}

func (w *usageClientWriter) Flush() {
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *usageResponseWriter) Header() http.Header { return w.target.Header() }

func (w *usageResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.target.WriteHeader(status)
}

func (w *usageResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.stream {
		if strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
			w.inspection.feed(body)
		}
		if (w.status < 200 || w.status > 299) && w.failureBody.Len() < 64<<10 {
			remaining := 64<<10 - w.failureBody.Len()
			_, _ = w.failureBody.Write(body[:min(len(body), remaining)])
		}
		w.consumeStream(body)
	} else if w.body.Len() < 8<<20 {
		remaining := 8<<20 - w.body.Len()
		w.body.Write(body[:min(len(body), remaining)])
	}
	n, err := w.target.Write(body)
	if err != nil && !errors.Is(err, errAutoStreamComplete) && !errors.Is(err, errAutoStreamRejected) {
		w.writeErr = err
	} else if n < len(body) && err == nil {
		w.writeErr = io.ErrShortWrite
	}
	return n, err
}

func (w *usageResponseWriter) Flush() {
	if flusher, ok := w.target.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *usageResponseWriter) consumeStream(body []byte) {
	w.pending = append(w.pending, body...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			if len(w.pending) > 1<<20 {
				w.pending = w.pending[len(w.pending)-(1<<20):]
			}
			return
		}
		line := append([]byte(nil), w.pending[:index]...)
		w.pending = w.pending[index+1:]
		w.consumeStreamLine(line)
	}
}

func (w *usageResponseWriter) finishStream() {
	if w.stream && strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		w.inspection.finish()
	}
	if len(w.pending) > 0 {
		w.consumeStreamLine(w.pending)
		w.pending = nil
	}
}

func (w *usageResponseWriter) consumeStreamLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	w.usage.merge(extractTokenUsage(data))
}

func extractTokenUsage(raw []byte) tokenUsage {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return tokenUsage{}
	}
	usage := anyMap(body["usage"])
	if len(usage) == 0 {
		usage = anyMap(anyMap(body["message"])["usage"])
	}
	if len(usage) == 0 {
		return tokenUsage{}
	}
	prompt, promptOK := usageInt(usage, "prompt_tokens", "input_tokens")
	completion, completionOK := usageInt(usage, "completion_tokens", "output_tokens")
	total, totalOK := usageInt(usage, "total_tokens")
	if !totalOK {
		total = prompt + completion
	}
	return tokenUsage{Prompt: prompt, Completion: completion, Total: total, Found: promptOK || completionOK || totalOK}
}

func usageFailureReason(status int, raw []byte) string {
	message := strings.TrimSpace(extractUsageErrorMessage(raw))
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	if message == "" {
		return formatProbeFailure(status, "")
	}
	return truncateString(message, 2000)
}

func extractUsageErrorMessage(raw []byte) string {
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	if value, ok := body["error"].(string); ok {
		return value
	}
	if nested := anyMap(body["error"]); len(nested) > 0 {
		for _, key := range []string{"message", "detail", "error_description", "errorDescription"} {
			if value, ok := nested[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	for _, key := range []string{"message", "detail", "error_description", "errorDescription"} {
		if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func usageInt(values map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch n := value.(type) {
		case float64:
			return int64(n), true
		case json.Number:
			v, err := n.Int64()
			return v, err == nil
		case int:
			return int64(n), true
		case int64:
			return n, true
		}
	}
	return 0, false
}

func (u *tokenUsage) merge(next tokenUsage) {
	if !next.Found {
		return
	}
	u.Found = true
	if next.Prompt != 0 {
		u.Prompt = next.Prompt
	}
	if next.Completion != 0 {
		u.Completion = next.Completion
	}
	if next.Total != 0 {
		u.Total = next.Total
	} else {
		u.Total = u.Prompt + u.Completion
	}
}

func addClientCounters(counters *UsageCounters, status int) {
	counters.ClientRequests++
	if status >= 200 && status <= 299 {
		counters.ClientSuccess++
	} else {
		counters.ClientFailed++
	}
}

func addUpstreamCounters(counters *UsageCounters, status int, usage tokenUsage) {
	counters.UpstreamCalls++
	if status >= 200 && status <= 299 {
		counters.UpstreamSuccess++
		if !usage.Found {
			counters.UnreportedCalls++
		}
	} else {
		counters.UpstreamFailed++
	}
	counters.PromptTokens += usage.Prompt
	counters.CompletionTokens += usage.Completion
	counters.TotalTokens += usage.Total
}

func (s *Server) usageGroupLocked(info usageRequestInfo) *UsageGroupStats {
	rollUsageDate(&s.usage)
	if s.usage.Groups == nil {
		s.usage.Groups = map[string]*UsageGroupStats{}
	}
	group := s.usage.Groups[info.GroupID]
	if group == nil {
		group = &UsageGroupStats{ID: info.GroupID, Models: map[string]*UsageModelStats{}}
		s.usage.Groups[info.GroupID] = group
	}
	group.Name = info.GroupName
	if group.Models == nil {
		group.Models = map[string]*UsageModelStats{}
	}
	return group
}

func (s *Server) recordClientUsage(info usageRequestInfo, status int) {
	s.usageMu.Lock()
	group := s.usageGroupLocked(info)
	addClientCounters(&group.Today, status)
	addClientCounters(&group.Total, status)
	s.usage.UpdatedAt = time.Now().Unix()
	s.usageMu.Unlock()
	s.scheduleUsageSave()
}

func (s *Server) recordUpstreamUsage(info usageRequestInfo, p ProviderConfig, model string, status int, usage tokenUsage, failureReason string, failureStatus int) {
	s.usageMu.Lock()
	group := s.usageGroupLocked(info)
	key := p.ID + "/" + model
	stats := group.Models[key]
	if stats == nil {
		stats = &UsageModelStats{ProviderID: p.ID, Model: model}
		group.Models[key] = stats
	}
	stats.ProviderName = p.Name
	addUpstreamCounters(&group.Today, status, usage)
	addUpstreamCounters(&group.Total, status, usage)
	addUpstreamCounters(&stats.Today, status, usage)
	addUpstreamCounters(&stats.Total, status, usage)
	if failureReason != "" {
		stats.LastFailure = failureReason
		stats.LastFailureAt = time.Now().Unix()
		stats.LastFailureStatus = failureStatus
	}
	s.usage.UpdatedAt = time.Now().Unix()
	s.usageMu.Unlock()
	s.scheduleUsageSave()
}

func (s *Server) scheduleUsageSave() {
	if s.usageSaveCh == nil {
		return
	}
	select {
	case s.usageSaveCh <- struct{}{}:
	default:
	}
}

func (s *Server) usageSaveLoop() {
	for range s.usageSaveCh {
		time.Sleep(500 * time.Millisecond)
		if err := s.saveUsage(); err != nil {
			log.Printf("save usage: %v", err)
		}
	}
}

func (s *Server) saveUsage() error {
	s.usageMu.Lock()
	rollUsageDate(&s.usage)
	pruneStreamHealth(&s.usage, time.Now())
	b, err := json.MarshalIndent(s.usage, "", "  ")
	s.usageMu.Unlock()
	if err != nil {
		return err
	}
	return saveJSONFile(path.Join(s.dataDir, usageFileName), append(b, '\n'))
}

func (s *Server) usageSnapshot() UsageStore {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	rollUsageDate(&s.usage)
	pruneStreamHealth(&s.usage, time.Now())
	b, _ := json.Marshal(s.usage)
	var snapshot UsageStore
	_ = json.Unmarshal(b, &snapshot)
	return snapshot
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hours := 24
	switch r.URL.Query().Get("stream_hours") {
	case "", "24":
	case "168":
		hours = 168
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "stream_hours must be 24 or 168"})
		return
	}
	snapshot := s.usageSnapshot()
	w.Header().Set("Cache-Control", "no-store")
	groups := make([]*UsageGroupStats, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Total.TotalTokens == groups[j].Total.TotalTokens {
			return groups[i].Name < groups[j].Name
		}
		return groups[i].Total.TotalTokens > groups[j].Total.TotalTokens
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"date": snapshot.Date, "updated_at": snapshot.UpdatedAt, "groups": groups,
		"stream_health": streamHealthRanking(snapshot, hours, time.Now()), "stream_hours": hours,
	})
}

var _ http.Flusher = (*usageResponseWriter)(nil)
var _ http.Flusher = (*usageClientWriter)(nil)
