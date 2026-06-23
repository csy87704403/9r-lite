package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type mimoProxyGroupState struct {
	All  []string `json:"all"`
	Now  string   `json:"now"`
	Type string   `json:"type"`
}

func (s *Server) handleMimoProxyNodes(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	state, err := s.fetchMimoProxyGroup(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"group":   s.mimoProxyGroup,
		"current": state.Now,
		"nodes":   usableMimoProxyNodes(state.All),
	})
}

func (s *Server) handleMimoProxyTestNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	node, err := decodeMimoProxyNode(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	latency, err := s.testMimoProxyNode(ctx, node)
	result := map[string]any{"ok": err == nil, "node": node, "latency_ms": latency}
	if err != nil {
		result["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleMimoProxySelect(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	node, err := decodeMimoProxyNode(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	s.mimoProxyMu.Lock()
	defer s.mimoProxyMu.Unlock()
	if err := s.selectMimoProxyNode(ctx, node); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	s.mimo.Reset()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": node})
}

func decodeMimoProxyNode(w http.ResponseWriter, r *http.Request) (string, error) {
	var body struct {
		Node string `json:"node"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		return "", err
	}
	body.Node = strings.TrimSpace(body.Node)
	if body.Node == "" {
		return "", errors.New("proxy node is required")
	}
	return body.Node, nil
}

func (s *Server) testMimoProxyNode(ctx context.Context, node string) (int64, error) {
	s.mimoProxyMu.Lock()
	defer s.mimoProxyMu.Unlock()
	if err := s.selectMimoProxyNode(ctx, node); err != nil {
		return 0, err
	}
	s.mimo.Reset()
	start := time.Now()
	err := s.probeSingleModel(ctx, "mmf", "mimo-auto")
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, err
	}
	p, ok := s.providerByID("mmf")
	if ok {
		p = updateProbeResult(p, "mimo-auto", nil, latency, false)
		if err := s.updateProvider(p); err != nil {
			return latency, err
		}
	}
	return latency, nil
}

func (s *Server) fetchMimoProxyGroup(ctx context.Context) (mimoProxyGroupState, error) {
	if strings.TrimSpace(s.mimoProxyControlURL) == "" {
		return mimoProxyGroupState{}, errors.New("MIMO_PROXY_CONTROL_URL is not configured")
	}
	endpoint := s.mimoProxyControlURL + "/proxies/" + url.PathEscape(s.mimoProxyGroup)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return mimoProxyGroupState{}, err
	}
	resp, err := s.mimoProxyControlClient.Do(req)
	if err != nil {
		return mimoProxyGroupState{}, fmt.Errorf("Mihomo controller unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return mimoProxyGroupState{}, fmt.Errorf("Mihomo controller returned HTTP %d", resp.StatusCode)
	}
	var state mimoProxyGroupState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&state); err != nil {
		return mimoProxyGroupState{}, fmt.Errorf("invalid Mihomo controller response")
	}
	return state, nil
}

func (s *Server) selectMimoProxyNode(ctx context.Context, node string) error {
	state, err := s.fetchMimoProxyGroup(ctx)
	if err != nil {
		return err
	}
	if !sliceSet(usableMimoProxyNodes(state.All))[node] {
		return errors.New("proxy node is not in the configured Mihomo group")
	}
	body, _ := json.Marshal(map[string]string{"name": node})
	endpoint := s.mimoProxyControlURL + "/proxies/" + url.PathEscape(s.mimoProxyGroup)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.mimoProxyControlClient.Do(req)
	if err != nil {
		return fmt.Errorf("Mihomo node switch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Mihomo node switch returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func usableMimoProxyNodes(nodes []string) []string {
	blocked := map[string]bool{"DIRECT": true, "REJECT": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true}
	var out []string
	for _, node := range uniqueStrings(nodes) {
		node = strings.TrimSpace(node)
		if node == "" || blocked[strings.ToUpper(node)] {
			continue
		}
		out = append(out, node)
	}
	return out
}
