package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminPasswordPersistsAndOverridesBootstrap(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "bootstrap-pass")
	dataDir := t.TempDir()
	s, err := NewServer(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.verifyAdminPassword("bootstrap-pass") {
		t.Fatal("bootstrap password should work before a password is saved")
	}

	oldCookie := s.adminCookieValue()
	if err := s.changeAdminPassword("bootstrap-pass", "new-admin-pass"); err != nil {
		t.Fatal(err)
	}
	if s.verifyAdminPassword("bootstrap-pass") || !s.verifyAdminPassword("new-admin-pass") {
		t.Fatal("saved password should replace the bootstrap password")
	}
	if oldCookie == s.adminCookieValue() {
		t.Fatal("changing the password should invalidate existing sessions")
	}

	b, err := os.ReadFile(filepath.Join(dataDir, adminAuthFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "new-admin-pass") {
		t.Fatal("admin auth file must not contain the plaintext password")
	}

	t.Setenv("ADMIN_PASSWORD", "different-bootstrap-pass")
	restarted, err := NewServer(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.verifyAdminPassword("new-admin-pass") {
		t.Fatal("saved password should remain valid after restart")
	}
	if restarted.verifyAdminPassword("different-bootstrap-pass") {
		t.Fatal("bootstrap password must not override a saved password")
	}
}

func TestAdminPasswordEndpointRequiresCurrentPassword(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "bootstrap-pass")
	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	request := func(current, next string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"current_password": current, "new_password": next})
		r := httptest.NewRequest(http.MethodPost, "/api/admin/password", strings.NewReader(string(body)))
		r.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
		w := httptest.NewRecorder()
		s.handleAdminPassword(w, r)
		return w
	}

	if got := request("wrong-password", "new-admin-pass").Code; got != http.StatusUnauthorized {
		t.Fatalf("wrong current password status = %d, want %d", got, http.StatusUnauthorized)
	}
	oldCookie := s.adminCookieValue()
	w := request("bootstrap-pass", "new-admin-pass")
	if w.Code != http.StatusOK {
		t.Fatalf("password change status = %d, body = %s", w.Code, w.Body.String())
	}
	oldRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	oldRequest.AddCookie(&http.Cookie{Name: "nr_admin", Value: oldCookie})
	if s.hasAdminSession(oldRequest) {
		t.Fatal("old session should be invalid after password change")
	}
}

func TestAdminHelpRequiresSession(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "bootstrap-pass")
	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "/admin/help", nil)
	unauthorized.Header.Set("Accept", "text/html")
	unauthorizedRecorder := httptest.NewRecorder()
	s.handleAdminHelp(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusFound {
		t.Fatalf("unauthorized help status = %d, want %d", unauthorizedRecorder.Code, http.StatusFound)
	}

	authorized := httptest.NewRequest(http.MethodGet, "/admin/help", nil)
	authorized.AddCookie(&http.Cookie{Name: "nr_admin", Value: s.adminCookieValue()})
	authorizedRecorder := httptest.NewRecorder()
	s.handleAdminHelp(authorizedRecorder, authorized)
	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized help status = %d", authorizedRecorder.Code)
	}
	if body := authorizedRecorder.Body.String(); !strings.Contains(body, "模型分组") || !strings.Contains(body, "Auto 模型") {
		t.Fatal("help page is missing the expected feature summary")
	}
}

func TestAdminHTMLIncludesPasswordAndHelpControls(t *testing.T) {
	html := adminHTMLLiteV2(`{"providers":[]}`)
	for _, want := range []string{"修改登录密码", "/admin/help", "changeAdminPassword()", "model-kind-select", "saveModelKind"} {
		if !strings.Contains(html, want) {
			t.Fatalf("admin HTML does not contain %q", want)
		}
	}
}
