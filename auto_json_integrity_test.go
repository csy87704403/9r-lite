package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAutoRetriesTruncatedOrInvalidNonStreamingJSON(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			var fallback atomic.Int32
			s, _ := regressionFailoverServer(t, func(w http.ResponseWriter, r *http.Request) {
				var req chatRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Model == "bad" {
					w.Header().Set("Content-Type", "application/json")
					if !malformed {
						w.Header().Set("Content-Length", "1000")
					}
					w.WriteHeader(http.StatusOK)
					if malformed {
						fmt.Fprint(w, `{"choices":[`)
					} else {
						fmt.Fprint(w, `{"choices":[]}`)
					}
					return
				}
				fallback.Add(1)
				writeJSON(w, http.StatusOK, probeTextResponse())
			}, false)
			s.usage = newUsageStore()
			w := regressionAutoRequest(t, s, `{"model":"auto","messages":[{"role":"user","content":"hello"}]}`)
			if w.Code != http.StatusOK || fallback.Load() != 1 || !json.Valid(w.Body.Bytes()) || s.autoRuntimeSnapshot()[0].LastResult != "failed" {
				t.Fatalf("broken JSON accepted: fallback=%d status=%d body=%s", fallback.Load(), w.Code, w.Body.String())
			}
			group := s.usageSnapshot().Groups["_master"]
			if group == nil || group.Total.UpstreamFailed != 1 || group.Total.UpstreamSuccess != 1 {
				t.Fatalf("broken JSON not reflected in usage: %+v", group)
			}
		})
	}
}

func TestRotatingKeysStillPassBinaryResponses(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0xff, 0x00, 0xfe, 0x01})
	}))
	defer u.Close()
	p := ProviderConfig{ID: "media", Type: "openai", APIKey: "one", APIKeys: []string{"two"}}
	s := &Server{dataDir: t.TempDir(), config: Config{Providers: []ProviderConfig{p}}, client: u.Client()}
	w := httptest.NewRecorder()
	s.proxyPostRotating(w, httptest.NewRequest(http.MethodPost, "/v1/tts", strings.NewReader("{}")), u.URL, []byte("{}"), p, providerAPIKeys(p), map[string]string{"Accept": "audio/mpeg"}, "music")
	if w.Code != http.StatusOK || w.Body.Len() != 4 || w.Body.Bytes()[0] != 0xff {
		t.Fatalf("JSON validation broke binary media: %d %s", w.Code, w.Body.String())
	}
}
