package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	qoderLoginURL       = "https://qoder.com/device/selectAccounts"
	qoderDeviceTokenURL = "https://openapi.qoder.sh/api/v1/deviceToken/poll"
	qoderUserInfoURL    = "https://openapi.qoder.sh/api/v1/userinfo"
	qoderModelListURL   = "https://api3.qoder.sh/algo/api/v2/model/list"
	qoderChatURL        = "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	qoderRSAPublicKey   = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`
)

type qoderDevicePollRequest struct {
	DeviceCode   string            `json:"deviceCode"`
	CodeVerifier string            `json:"codeVerifier"`
	ExtraData    map[string]string `json:"extraData"`
}

type qoderCatalogEntry struct {
	expiresAt   time.Time
	models      []string
	rawConfigs  map[string]map[string]any
	displayName map[string]string
}

var qoderCatalogCache = struct {
	sync.Mutex
	entries map[string]qoderCatalogEntry
}{entries: map[string]qoderCatalogEntry{}}

func qoderStaticModels() []string {
	return []string{
		"auto",
		"ultimate",
		"performance",
		"efficient",
		"lite",
		"qmodel",
		"qmodel_latest",
		"dmodel",
		"dfmodel",
		"gm51model",
		"kmodel",
		"mmodel",
	}
}

func (s *Server) handleQoderDeviceCode(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	verifier, challenge, err := qoderPKCE()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	nonce := uuidV4()
	machineID := uuidV4()
	params := url.Values{}
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("machine_id", machineID)
	params.Set("nonce", nonce)

	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               nonce,
		"user_code":                 strings.ToUpper(nonce[:8]),
		"verification_uri":          qoderLoginURL,
		"verification_uri_complete": qoderLoginURL + "?" + params.Encode(),
		"expires_in":                300,
		"interval":                  2,
		"codeVerifier":              verifier,
		"_qoderNonce":               nonce,
		"_qoderMachineId":           machineID,
	})
}

func (s *Server) handleQoderPoll(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body qoderDevicePollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	nonce := firstNonEmpty(body.DeviceCode, body.ExtraData["_qoderNonce"])
	verifier := firstNonEmpty(body.CodeVerifier, body.ExtraData["_qoderVerifier"])
	if nonce == "" || verifier == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "invalid_request", "errorDescription": "Missing nonce/verifier"})
		return
	}

	result, err := qoderPollDeviceToken(r.Context(), s.client, nonce, verifier)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": "poll_failed", "errorDescription": err.Error()})
		return
	}
	if result["status"] == "pending" {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "pending": true, "error": "authorization_pending"})
		return
	}

	accessToken := stringValue(result["accessToken"])
	userID := stringValue(result["userId"])
	if accessToken == "" || userID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "error": "invalid_response", "errorDescription": "Qoder returned no token/userId"})
		return
	}
	userInfo := qoderFetchUserInfo(r.Context(), s.client, accessToken)
	expiresAt := qoderParseExpiry(result["expiresAt"], result["expiresIn"])
	expiresIn := int64(time.Until(expiresAt).Seconds())
	if expiresIn < 86400 {
		expiresIn = 86400
	}

	p, _ := s.providerByID("qoder")
	if p.ID == "" {
		p = ProviderConfig{ID: "qoder", Name: "Qoder Free", Type: "qoder", Models: qoderStaticModels()}
	}
	p.Type = "qoder"
	p.Enabled = true
	p.AccessToken = accessToken
	p.RefreshToken = stringValue(result["refreshToken"])
	p.ExpiresIn = expiresIn
	p.DisplayName = stringValue(userInfo["name"])
	p.Email = firstNonEmpty(stringValue(userInfo["email"]), "qoder-user-"+userID)
	p.ProviderSpecificData = map[string]string{
		"authMethod":     "device",
		"userId":         userID,
		"machineId":      firstNonEmpty(body.ExtraData["_qoderMachineId"], body.ExtraData["machineId"]),
		"organizationId": stringValue(userInfo["organizationId"]),
	}
	if len(p.Models) == 0 {
		p.Models = qoderStaticModels()
	}
	if err := s.updateProvider(p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "provider": p})
}

func qoderPollDeviceToken(ctx context.Context, client *http.Client, nonce, verifier string) (map[string]any, error) {
	u := qoderDeviceTokenURL + "?nonce=" + url.QueryEscape(nonce) + "&verifier=" + url.QueryEscape(verifier) + "&challenge_method=S256"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNotFound {
		return map[string]any{"status": "pending"}, nil
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Qoder device token poll failed: HTTP %d: %s", resp.StatusCode, truncateString(string(b), 200))
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	token := stringValue(raw["token"])
	if token == "" {
		return nil, errors.New("Qoder device token poll returned 200 but no token")
	}
	return map[string]any{
		"status":       "ok",
		"accessToken":  token,
		"refreshToken": stringValue(raw["refresh_token"]),
		"userId":       stringValue(raw["user_id"]),
		"expiresAt":    raw["expires_at"],
		"expiresIn":    raw["expires_in"],
	}, nil
}

func qoderFetchUserInfo(ctx context.Context, client *http.Client, accessToken string) map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderUserInfoURL, nil)
	if err != nil {
		return map[string]any{}
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return map[string]any{}
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return map[string]any{}
	}
	return map[string]any{
		"name":           strings.TrimSpace(firstNonEmpty(stringValue(raw["name"]), stringValue(raw["username"]))),
		"email":          strings.TrimSpace(stringValue(raw["email"])),
		"organizationId": strings.TrimSpace(stringValue(raw["organization_id"])),
	}
}

func fetchQoderModels(ctx context.Context, client *http.Client, p ProviderConfig, force bool) ([]string, error) {
	entry, err := qoderCatalog(ctx, client, p, force)
	if err != nil {
		return nil, err
	}
	return entry.models, nil
}

func qoderCatalog(ctx context.Context, client *http.Client, p ProviderConfig, force bool) (qoderCatalogEntry, error) {
	key := qoderCacheKey(p)
	now := time.Now()
	qoderCatalogCache.Lock()
	if !force {
		if cached, ok := qoderCatalogCache.entries[key]; ok && cached.expiresAt.After(now) {
			qoderCatalogCache.Unlock()
			return cached, nil
		}
	}
	qoderCatalogCache.Unlock()

	headers, err := qoderCosyHeaders(nil, qoderModelListURL, p)
	if err != nil {
		return qoderCatalogEntry{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderModelListURL, nil)
	if err != nil {
		return qoderCatalogEntry{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return qoderCatalogEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return qoderCatalogEntry{}, fmt.Errorf("qoder model list status %d", resp.StatusCode)
	}
	var raw struct {
		Chat []map[string]any `json:"chat"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return qoderCatalogEntry{}, err
	}
	entry := qoderCatalogEntry{
		expiresAt:   now.Add(time.Hour),
		rawConfigs:  map[string]map[string]any{},
		displayName: map[string]string{},
	}
	for _, item := range raw.Chat {
		id := stringValue(item["key"])
		if id == "" {
			continue
		}
		entry.rawConfigs[id] = cloneMap(item)
		entry.displayName[id] = firstNonEmpty(stringValue(item["display_name"]), id)
		if enabled, ok := item["enable"].(bool); ok && !enabled {
			continue
		}
		entry.models = append(entry.models, id)
	}
	if len(entry.models) == 0 {
		entry.models = qoderStaticModels()
	}
	entry.models = uniqueStrings(entry.models)
	sort.Strings(entry.models)

	qoderCatalogCache.Lock()
	qoderCatalogCache.entries[key] = entry
	qoderCatalogCache.Unlock()
	return entry, nil
}

func (s *Server) proxyQoder(w http.ResponseWriter, r *http.Request, p ProviderConfig, req chatRequest, upstreamModel string) {
	if p.AccessToken == "" || p.ProviderSpecificData["userId"] == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "qoder is not logged in; open /api/oauth/qoder/device-code first"})
		return
	}
	payload, err := s.buildQoderPayload(r.Context(), p, req.Raw, upstreamModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	plain, _ := json.Marshal(payload)
	encoded := []byte(qoderEncodeBody(plain))
	headers, err := qoderCosyHeaders(encoded, qoderChatURL, p)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, qoderChatURL, bytes.NewReader(encoded))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "text/event-stream")
	upReq.Header.Set("Cache-Control", "no-cache")
	upReq.Header.Set("X-Model-Key", upstreamModel)
	upReq.Header.Set("Accept-Encoding", "identity")
	for k, v := range headers {
		upReq.Header.Set(k, v)
	}
	resp, err := s.client.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	defer s.client.CloseIdleConnections()
	defer debugFree()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			s.markProviderAuthState(p.ID, "needs_login", fmt.Sprintf("Qoder returned %d; please login again", resp.StatusCode))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	s.markProviderAuthState(p.ID, "ok", "")
	if req.Stream {
		qoderStreamToOpenAI(w, resp.Body)
		return
	}
	qoderCollectToJSON(w, resp.Body, "qoder/"+upstreamModel)
}

func (s *Server) buildQoderPayload(ctx context.Context, p ProviderConfig, raw []byte, model string) (map[string]any, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	entry, err := qoderCatalog(ctx, s.client, p, false)
	if err != nil {
		entry, err = qoderCatalog(ctx, s.client, p, true)
	}
	if err != nil {
		return nil, err
	}
	modelConfig := entry.rawConfigs[model]
	if modelConfig == nil {
		return nil, fmt.Errorf("qoder: model_config for %q not found", model)
	}
	messages, systemText := qoderNormalizeMessages(body["messages"])
	maxTokens := qoderMaxTokens(body, modelConfig)
	lastUser := qoderLastUser(messages)
	recordID := qoderStableHash("qoder-record", model, lastUser, fmt.Sprint(maxTokens))
	sessionID := qoderStableHash("qoder-session", p.ProviderSpecificData["userId"], model)

	return map[string]any{
		"request_id":     uuidV4(),
		"request_set_id": recordID,
		"chat_record_id": recordID,
		"session_id":     sessionID,
		"stream":         true,
		"chat_task":      "FREE_INPUT",
		"is_reply":       true,
		"is_retry":       false,
		"source":         1,
		"version":        "3",
		"session_type":   "qodercli",
		"agent_id":       "agent_common",
		"task_id":        "common",
		"code_language":  "",
		"chat_prompt":    "",
		"image_urls":     nil,
		"system":         systemText,
		"messages":       messages,
		"tools":          qoderTools(body["tools"]),
		"parameters":     map[string]any{"max_tokens": maxTokens},
		"chat_context": map[string]any{
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     map[string]any{"key": model, "is_reasoning": boolValue(modelConfig["is_reasoning"])},
				"originalContent": lastUser,
			},
			"features": []any{},
			"text":     lastUser,
		},
		"model_config": modelConfig,
		"business": map[string]any{
			"product":  "cli",
			"version":  "1.0.0",
			"type":     "agent",
			"stage":    "start",
			"id":       uuidV4(),
			"name":     truncateString(lastUser, 30),
			"begin_at": time.Now().UnixMilli(),
		},
	}, nil
}

func qoderStreamToOpenAI(w http.ResponseWriter, r io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		inner, done, ok := qoderUnwrapLine(scanner.Text())
		if !ok {
			continue
		}
		if done {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		_, _ = w.Write([]byte("data: " + strings.ReplaceAll(inner, "\n", "") + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func qoderCollectToJSON(w http.ResponseWriter, r io.Reader, model string) {
	var content strings.Builder
	var reasoning strings.Builder
	finish := "stop"
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		inner, done, ok := qoderUnwrapLine(scanner.Text())
		if !ok {
			continue
		}
		if done {
			break
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(inner), &chunk) != nil {
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if f := stringValue(choice["finish_reason"]); f != "" {
			finish = f
		}
		delta, _ := choice["delta"].(map[string]any)
		content.WriteString(stringValue(delta["content"]))
		reasoning.WriteString(stringValue(delta["reasoning_content"]))
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "qoder-" + uuidV4(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	})
}

func qoderUnwrapLine(line string) (inner string, done bool, ok bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if !strings.HasPrefix(line, "data:") {
		return "", false, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return "", true, true
	}
	var env struct {
		StatusCodeValue int    `json:"statusCodeValue"`
		Body            string `json:"body"`
	}
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		return "", false, false
	}
	if env.StatusCodeValue != 0 && env.StatusCodeValue != 200 {
		errChunk, _ := json.Marshal(map[string]any{
			"id":      "qoder-error-" + uuidV4(),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "\n[qoder error " + strconv.Itoa(env.StatusCodeValue) + "]"}, "finish_reason": "stop"}},
		})
		return string(errChunk), false, true
	}
	if env.Body == "" {
		return "", false, false
	}
	if env.Body == "[DONE]" {
		return "", true, true
	}
	return env.Body, false, true
}

func qoderCosyHeaders(body []byte, requestURL string, p ProviderConfig) (map[string]string, error) {
	userID := p.ProviderSpecificData["userId"]
	if userID == "" {
		return nil, errors.New("cosy: user id is empty")
	}
	if p.AccessToken == "" {
		return nil, errors.New("cosy: auth token is empty")
	}
	aesKey := uuidV4()[:16]
	infoJSON, _ := json.Marshal(map[string]string{
		"uid":                  userID,
		"security_oauth_token": p.AccessToken,
		"name":                 p.DisplayName,
		"aid":                  "",
		"email":                p.Email,
	})
	infoB64, err := aesEncryptCBCBase64(infoJSON, aesKey)
	if err != nil {
		return nil, err
	}
	cosyKeyB64, err := rsaEncryptBase64([]byte(aesKey))
	if err != nil {
		return nil, err
	}
	requestID := uuidV4()
	payloadJSON, _ := json.Marshal(map[string]string{
		"version":     "v1",
		"requestId":   requestID,
		"info":        infoB64,
		"cosyVersion": "1.0.0",
		"ideVersion":  "",
	})
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sigPath := qoderSigPath(requestURL)
	sigInput := payloadB64 + "\n" + cosyKeyB64 + "\n" + timestamp + "\n" + string(body) + "\n" + sigPath
	sig := md5Hex([]byte(sigInput))
	machineID := firstNonEmpty(p.ProviderSpecificData["machineId"], uuidV4())
	return map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKeyB64,
		"Cosy-User":              userID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           "1.0.0",
		"Cosy-Machineid":         machineID,
		"Cosy-Machinetoken":      machineID,
		"Cosy-Machinetype":       "5",
		"Cosy-Machineos":         "x86_64_windows",
		"Cosy-Clienttype":        "5",
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          md5Hex(body),
		"Cosy-Bodylength":        strconv.Itoa(len(body)),
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           uuidV4(),
	}, nil
}

func qoderEncodeBody(plain []byte) string {
	stdAlphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customAlphabet := "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	table := map[byte]byte{'=': '$'}
	for i := 0; i < len(stdAlphabet); i++ {
		table[stdAlphabet[i]] = customAlphabet[i]
	}
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	a := n / 3
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	out := make([]byte, len(rearranged))
	for i := range rearranged {
		if mapped, ok := table[rearranged[i]]; ok {
			out[i] = mapped
		} else {
			out[i] = rearranged[i]
		}
	}
	return string(out)
}

func aesEncryptCBCBase64(plain []byte, key string) (string, error) {
	keyBytes := []byte(key)
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, keyBytes[:aes.BlockSize]).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func rsaEncryptBase64(data []byte) (string, error) {
	block, _ := pem.Decode([]byte(qoderRSAPublicKey))
	if block == nil {
		return "", errors.New("invalid qoder rsa public key")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("qoder rsa key is not RSA public key")
	}
	encrypted, err := rsa.EncryptPKCS1v15(crand.Reader, pub, data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func qoderPKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = crand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func qoderNormalizeMessages(raw any) ([]any, string) {
	arr, _ := raw.([]any)
	var out []any
	var system []string
	for _, item := range arr {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := qoderExtractText(msg["content"])
		role := stringValue(msg["role"])
		if role == "system" {
			if text != "" {
				system = append(system, text)
			}
			continue
		}
		cloned := cloneMap(msg)
		cloned["content"] = text
		out = append(out, cloned)
	}
	return out, strings.Join(system, "\n\n")
}

func qoderExtractText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	arr, ok := content.([]any)
	if !ok {
		return stringValue(content)
	}
	var parts []string
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if text := stringValue(m["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func qoderLastUser(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, _ := messages[i].(map[string]any)
		if stringValue(msg["role"]) == "user" {
			return stringValue(msg["content"])
		}
	}
	return ""
}

func qoderMaxTokens(body, modelConfig map[string]any) int {
	maxTokens := 32768
	if n := intValue(modelConfig["max_output_tokens"]); n > 0 {
		maxTokens = n
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if n := intValue(body[key]); n > 0 && n < maxTokens {
			maxTokens = n
		}
	}
	return maxTokens
}

func qoderTools(raw any) any {
	if arr, ok := raw.([]any); ok {
		return arr
	}
	return []any{}
}

func qoderStableHash(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func qoderCacheKey(p ProviderConfig) string {
	return qoderStableHash("qoder", firstNonEmpty(p.ProviderSpecificData["userId"], p.AccessToken))
}

func qoderSigPath(requestURL string) string {
	u, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(u.Path, "/algo") {
		return strings.TrimPrefix(u.Path, "/algo")
	}
	return u.Path
}

func qoderParseExpiry(expiresAt any, expiresIn any) time.Time {
	switch v := expiresAt.(type) {
	case float64:
		if v > 0 {
			return time.UnixMilli(int64(v))
		}
	case string:
		v = strings.TrimSpace(v)
		if v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				return time.UnixMilli(n)
			}
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	if n := intValue(expiresIn); n >= 0 {
		return time.Now().Add(time.Duration(n) * time.Second)
	}
	return time.Now().Add(30 * 24 * time.Hour)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+padding)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(padding)
	}
	return out
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func uuidV4() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func debugFree() {
	debug.FreeOSMemory()
}
