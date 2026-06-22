package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const adminAuthFileName = "admin-auth.json"

type adminAuthFile struct {
	PasswordHash string `json:"password_hash"`
}

func loadAdminPasswordHash(dataDir string) ([]byte, error) {
	b, err := os.ReadFile(path.Join(dataDir, adminAuthFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved adminAuthFile
	if err := json.Unmarshal(b, &saved); err != nil {
		return nil, err
	}
	hash := []byte(strings.TrimSpace(saved.PasswordHash))
	if len(hash) == 0 {
		return nil, errors.New("admin-auth.json does not contain a password hash")
	}
	if _, err := bcrypt.Cost(hash); err != nil {
		return nil, errors.New("admin-auth.json contains an invalid password hash")
	}
	return hash, nil
}

func saveAdminPasswordHash(dataDir string, hash []byte) error {
	b, err := json.MarshalIndent(adminAuthFile{PasswordHash: string(hash)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path.Join(dataDir, adminAuthFileName), append(b, '\n'), 0600)
}

func (s *Server) bootstrapAdminPassword() string {
	if v := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")); v != "" {
		return v
	}
	if v := strings.TrimSpace(s.currentConfig().AccessKey); v != "" {
		return v
	}
	return "123456"
}

func (s *Server) verifyAdminPassword(password string) bool {
	password = strings.TrimSpace(password)
	s.adminMu.RLock()
	hash := append([]byte(nil), s.adminHash...)
	s.adminMu.RUnlock()
	if len(hash) > 0 {
		return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	}
	expected := s.bootstrapAdminPassword()
	return subtle.ConstantTimeCompare([]byte(password), []byte(expected)) == 1
}

func (s *Server) changeAdminPassword(currentPassword, newPassword string) error {
	currentPassword = strings.TrimSpace(currentPassword)
	newPassword = strings.TrimSpace(newPassword)
	if !s.verifyAdminPassword(currentPassword) {
		return errors.New("当前密码错误")
	}
	if len(newPassword) < 8 {
		return errors.New("新密码至少需要 8 个字符")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := saveAdminPasswordHash(s.dataDir, hash); err != nil {
		return err
	}
	s.adminMu.Lock()
	s.adminHash = append([]byte(nil), hash...)
	s.adminSecret = newSessionID()
	s.adminMu.Unlock()
	return nil
}

func clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "nr_admin",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求内容无效"})
		return
	}
	if err := s.changeAdminPassword(body.CurrentPassword, body.NewPassword); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "当前密码错误" {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	clearAdminCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "relogin": true})
}

func (s *Server) handleAdminHelp(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderAdminHelpHTML()))
}

func renderAdminHelpHTML() string {
	return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>9Router Lite 说明</title>
<style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;margin:0;background:#fafafa;color:#111}main{max-width:820px;margin:32px auto;padding:0 20px}h1{font-size:28px;margin:0}h2{font-size:18px;margin:0 0 8px}.bar{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:20px}.card{background:#fff;border:1px solid #ddd;border-radius:8px;padding:16px;margin:12px 0}.muted{color:#666;font-size:13px;line-height:1.6}code{background:#eee;padding:2px 5px;border-radius:4px}a{color:#111;border:1px solid #ddd;border-radius:6px;padding:8px 12px;text-decoration:none;background:#fff}p{margin:7px 0;line-height:1.6}</style>
</head><body><main><div class="bar"><div><h1>9Router Lite</h1><div class="muted">轻量模型网关使用说明</div></div><a href="/admin">返回管理页</a></div>
<section class="card"><h2>接口与提供商</h2><p>统一提供 OpenAI 兼容接口 <code>/v1</code> 和 Anthropic 兼容接口 <code>/anthropic</code>，可接入 API Key、OAuth、OpenAI Responses、标准 Anthropic 与 Claude Code 兼容渠道。</p></section>
<section class="card"><h2>模型发布</h2><p>拉取模型后，可在单个渠道内进行真实调用探测并选择发布。只有发布的模型会进入对应公开模型列表；锁定模型会被一键探测和定时探测跳过。</p></section>
<section class="card"><h2>模型分组</h2><p>每个分组可设置独立 API Key，并限制该 Key 只能查看和调用指定模型。对外 Base URL 保持不变。</p></section>
<section class="card"><h2>Auto 模型</h2><p><code>auto</code> 按候选顺序选择可用模型。图片会话可由多模态候选模型接管；图片退出上下文后恢复普通候选列表。</p></section>
<section class="card"><h2>多媒体</h2><p>图片、视频、音频和 TTS 使用各渠道保存的完整 Endpoint 与 curl 模板。管理页可按已发布模型选择并复制调用示例。</p></section>
<section class="card"><h2>运行状态</h2><p><code>/health</code> 展示连接源、发布模型、Auto 状态与多媒体模板。JSON 输出适合程序读取，HTML 输出适合浏览器查看。</p></section>
</main></body></html>`
}
