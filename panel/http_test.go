package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(a http.Handler, method, path, host, origin, body, cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func TestHTTPAuthHostOriginCSRFAndAssets(t *testing.T) {
	s, _ := fixture(t)
	a, e := newApp(s, "test-password-long", false, false)
	if e != nil {
		t.Fatal(e)
	}
	host := "127.0.0.1:8088"
	origin := "http://" + host
	if w := request(a, "GET", "/api/state", host, "", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, p := range []string{"/", "/app.js", "/style.css", "/brand/controldom-logo.png", "/brand/inter-cyrillic.woff2", "/brand/inter-latin.woff2", "/brand/manrope-cyrillic.woff2", "/brand/manrope-latin.woff2"} {
		w := request(a, "GET", p, host, "", "", "", "")
		if w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal(p, w.Code)
		}
		if strings.HasSuffix(p, ".woff2") && w.Header().Get("Content-Type") != "font/woff2" {
			t.Fatal("font MIME type missing", p)
		}
	}
	if w := request(a, "GET", "/api/session", "evil.invalid", "", "", "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(a, "POST", "/api/login", host, "http://evil.invalid", `{"password":"test-password-long"}`, "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := request(a, "POST", "/api/login", host, origin, `{"password":"test-password-long"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	cookie := cookies[0].Name + "=" + cookies[0].Value
	var login Row
	_ = json.Unmarshal(w.Body.Bytes(), &login)
	csrf := str(login["csrf"])
	if w = request(a, "POST", "/api/switch", host, origin, `{"interfaces":["sp-demo1"],"enabled":true}`, cookie, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = request(a, "POST", "/api/switch", host, origin, `{"interfaces":["wifi1"],"enabled":false}`, cookie, csrf); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = request(a, "POST", "/api/switch", host, origin, `{"interfaces":["sp-demo1"],"enabled":true}`, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/switch", host, origin, `{"interfaces":["sp-demo1"],"enabled":"true"}`, cookie, csrf); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = request(a, "GET", "/api/state", host, "", "", cookie, ""); w.Code != 200 || strings.Contains(w.Body.String(), "special") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/login", host, origin, strings.Repeat("x", 17000), "", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = request(a, "GET", "/state.json", host, "", "", cookie, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestLoginRateLimit(t *testing.T) {
	s, _ := fixture(t)
	a, e := newApp(s, "test-password-long", false, false)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 6; i++ {
		w := request(a, "POST", "/api/login", "127.0.0.1:8088", "http://127.0.0.1:8088", `{"password":"wrong"}`, "", "")
		want := 401
		if i == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("%d: %d", i, w.Code)
		}
	}
}

func TestHTTPScanRequiresExplicitAcknowledgement(t *testing.T) {
	s, f := fixture(t)
	a, err := newApp(s, "test-password", false, false)
	if err != nil {
		t.Fatal(err)
	}
	host, origin := "127.0.0.1:8088", "http://127.0.0.1:8088"
	w := request(a, "POST", "/api/login", host, origin, `{"password":"test-password"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	c := w.Result().Cookies()[0]
	var login Row
	_ = json.Unmarshal(w.Body.Bytes(), &login)
	for _, path := range []string{"/api/scan", "/api/provision"} {
		w = request(a, "POST", path, host, origin, `{}`, c.Name+"="+c.Value, str(login["csrf"]))
		if w.Code != 400 || len(f.calls) != 0 {
			t.Fatal("operation without acknowledgement", path, w.Code)
		}
	}
}

func TestFiveCharacterPanelPassword(t *testing.T) {
	s, _ := fixture(t)
	for _, password := range []string{"", "1234"} {
		if _, err := newApp(s, password, false, false); err == nil {
			t.Fatal("accepted a password shorter than five characters")
		}
	}
	a, err := newApp(s, "ADMIN", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for password, want := range map[string]int{"ADMIN": 200, "admin": 401, "previous-password": 401} {
		body, _ := json.Marshal(Row{"password": password})
		w := request(a, "POST", "/api/login", "127.0.0.1:8088", "http://127.0.0.1:8088", string(body), "", "")
		if w.Code != want {
			t.Fatalf("login status = %d, want %d", w.Code, want)
		}
	}
}
func TestRouterErrorsDoNotEchoSecretsAndRedirectsAreBlocked(t *testing.T) {
	secret := "secret-wifi-password"
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true; _, _ = io.WriteString(w, `[]`) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "redirect") {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.WriteHeader(500)
		_, _ = io.WriteString(w, secret)
	}))
	defer source.Close()
	r, e := newRouter(source.URL, "panel", "router-password", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Call("POST", "tool/fetch", Row{"http-data": secret}); e == nil || strings.Contains(e.Error(), secret) {
		t.Fatal(e)
	}
	if _, e = r.Call("GET", "redirect", nil); e == nil || redirected {
		t.Fatal("redirect followed")
	}
}
func TestRouterFetchUsesJSONBodyAndDecodesRESTResponse(t *testing.T) {
	password := "a$\"\\&complex"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		if !ok || user != "panel" || pass != "router-password" || req.Method != "POST" || req.URL.Path != "/rest/tool/fetch" {
			t.Error("unexpected REST request")
		}
		var body Row
		if e := json.NewDecoder(req.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if str(body["url"]) != "http://192.168.33.1/rpc" || str(body["http-method"]) != "post" || str(body["http-header-field"]) != "Content-Type:application/json" {
			t.Error("unexpected fetch request")
		}
		var rpc struct {
			Method string
			Params struct {
				Config struct{ Sta struct{ Pass string } }
			}
		}
		if e := json.Unmarshal([]byte(str(body["http-data"])), &rpc); e != nil || rpc.Method != "WiFi.SetConfig" || rpc.Params.Config.Sta.Pass != password {
			t.Error("Wi-Fi password JSON did not round-trip")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Row{{"status": "finished", "data": `{"id":1,"result":{"restart_required":false}}`}})
	}))
	defer server.Close()
	r, e := newRouter(server.URL, "panel", "router-password", "")
	if e != nil {
		t.Fatal(e)
	}
	result, e := r.RPC("192.168.33.1", "WiFi.SetConfig", Row{"config": Row{"sta": Row{"pass": password}}})
	if e != nil || result["restart_required"] != false {
		t.Fatal(result, e)
	}
}
