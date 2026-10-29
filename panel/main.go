package main

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	cliFlag "flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed static/index.html static/app.js static/style.css static/brand/*
var assets embed.FS

const panelVersion = "2026.10.29.6"

type Session struct {
	CSRF    string
	Expires time.Time
}
type App struct {
	service      *Service
	hosts        map[string]bool
	demo, secure bool
	salt, hash   []byte
	mu           sync.Mutex
	sessions     map[string]Session
	attempts     map[string][]time.Time
}

func newApp(s *Service, password string, demo, secure bool) (*App, error) {
	if len(password) < 5 {
		return nil, errors.New("PANEL_PASSWORD трябва да има поне 5 знака")
	}
	salt := []byte(token())
	hash, e := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if e != nil {
		return nil, e
	}
	a := &App{service: s, hosts: map[string]bool{}, demo: demo, secure: secure, salt: salt, hash: hash, sessions: map[string]Session{}, attempts: map[string][]time.Time{}}
	for _, v := range s.config.Hosts {
		a.hosts[v] = true
	}
	if len(a.hosts) == 0 {
		return nil, errors.New("Задай allowed_hosts в config.json")
	}
	return a, nil
}
func (a *App) session(r *http.Request) (string, Session, bool) {
	cookie, e := r.Cookie("sp_session")
	if e != nil {
		return "", Session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.sessions {
		if !now.Before(v.Expires) {
			delete(a.sessions, k)
		}
	}
	s, ok := a.sessions[cookie.Value]
	return cookie.Value, s, ok
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, m string) { reply(w, status, Row{"error": m}) }
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
	if !a.hosts[r.Host] {
		fail(w, 403, "Host не е разрешен")
		return
	}
	if r.Method == "GET" {
		a.get(w, r)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "Невалиден HTTP метод")
		return
	}
	scheme := "http"
	if a.secure {
		scheme = "https"
	}
	if r.Header.Get("Origin") != scheme+"://"+r.Host {
		fail(w, 403, "Невалиден Origin/Host")
		return
	}
	mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "application/json" {
		fail(w, 415, "Изисква се application/json")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	var body Row
	if e = dec.Decode(&body); e != nil || body == nil {
		fail(w, 400, "Невалиден JSON или размер на заявката")
		return
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		fail(w, 400, "Изисква се един JSON обект")
		return
	}
	if r.URL.Path == "/api/login" {
		a.login(w, r, body)
		return
	}
	id, session, ok := a.session(r)
	if !ok {
		fail(w, 401, "Необходим е вход")
		return
	}
	if !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) {
		fail(w, 403, "Невалиден CSRF token")
		return
	}
	var out Row
	switch r.URL.Path {
	case "/api/logout":
		a.mu.Lock()
		delete(a.sessions, id)
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "sp_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secure})
		out = Row{}
	case "/api/projects":
		out, e = a.service.saveProject(body)
	case "/api/password":
		out, e = a.service.networkPassword(str(body["interface"]))
	case "/api/wps":
		if body["stop"] == true {
			out, e = a.service.stopWPS()
		} else {
			out, e = a.service.startWPS(str(body["interface"]))
		}
	case "/api/probe":
		out, e = a.service.probeNetwork(str(body["interface"]))
	case "/api/script":
		if body["acknowledge_disconnect"] != true {
			e = errors.New("Потвърди използването на 5 GHz или LAN и временната пауза на 2.4 GHz")
		} else {
			out, e = a.service.startScript(body)
		}
	case "/api/wifi-scan":
		if body["acknowledge_disconnect"] != true {
			e = errors.New("Потвърди използването на 5 GHz или LAN и временната пауза на 2.4 GHz")
		} else {
			out, e = a.service.startScript(Row{"scan_only": true, "networks_only": true, "scan_seconds": body["scan_seconds"]})
		}
	case "/api/switch":
		var names []string
		var on bool
		names, e = stringList(body["interfaces"])
		if e == nil {
			var ok bool
			on, ok = body["enabled"].(bool)
			if !ok {
				e = errors.New("enabled трябва да е true или false")
			} else {
				out, e = a.service.switchNetworks(names, on)
			}
		}
	case "/api/scan", "/api/provision":
		if body["acknowledge_disconnect"] != true {
			e = errors.New("Потвърди временната пауза на 2.4 GHz")
		} else {
			out, e = a.service.start(strings.TrimPrefix(r.URL.Path, "/api/"), body)
		}
	case "/api/stop":
		a.service.cancel.Store(true)
		out = Row{"stopping": true}
	case "/api/recover":
		if !a.service.gate.TryLock() {
			e = errors.New("Изчакай активната операция")
		} else {
			e = a.service.recover()
			a.service.gate.Unlock()
			out = Row{"recovered": true}
		}
	default:
		fail(w, 404, "Няма такава операция")
		return
	}
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	reply(w, 200, out)
}
func (a *App) get(w http.ResponseWriter, r *http.Request) {
	asset := map[string]string{
		"/": "index.html", "/app.js": "app.js", "/style.css": "style.css",
		"/brand/controldom-logo.png":    "brand/controldom-logo.png",
		"/brand/inter-cyrillic.woff2":   "brand/inter-cyrillic.woff2",
		"/brand/inter-latin.woff2":      "brand/inter-latin.woff2",
		"/brand/manrope-cyrillic.woff2": "brand/manrope-cyrillic.woff2",
		"/brand/manrope-latin.woff2":    "brand/manrope-latin.woff2",
		"/brand/OFL-Inter.txt":          "brand/OFL-Inter.txt", "/brand/OFL-Manrope.txt": "brand/OFL-Manrope.txt",
	}[r.URL.Path]
	if asset != "" {
		b, e := assets.ReadFile("static/" + asset)
		if e != nil {
			fail(w, 500, "Липсва ресурс")
			return
		}
		contentType := mime.TypeByExtension(filepath.Ext(asset))
		if filepath.Ext(asset) == ".woff2" {
			contentType = "font/woff2"
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(200)
		_, _ = w.Write(b)
		return
	}
	_, session, ok := a.session(r)
	if r.URL.Path == "/api/session" {
		var csrf any
		if ok {
			csrf = session.CSRF
		}
		reply(w, 200, Row{"authenticated": ok, "csrf": csrf, "demo": a.demo, "version": panelVersion})
		return
	}
	if !ok {
		fail(w, 401, "Необходим е вход")
		return
	}
	switch r.URL.Path {
	case "/api/job":
		reply(w, 200, a.service.jobView())
	case "/api/telemetry":
		out, e := a.service.telemetry()
		if e != nil {
			fail(w, 502, e.Error())
		} else {
			reply(w, 200, out)
		}
	case "/api/state":
		out, e := a.service.overview()
		if e != nil {
			fail(w, 502, e.Error())
		} else {
			reply(w, 200, out)
		}
	default:
		fail(w, 404, "Няма такъв адрес")
	}
}
func (a *App) login(w http.ResponseWriter, r *http.Request, body Row) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	a.mu.Lock()
	for k, v := range a.attempts {
		fresh := v[:0]
		for _, t := range v {
			if now.Sub(t) < time.Minute {
				fresh = append(fresh, t)
			}
		}
		if len(fresh) == 0 {
			delete(a.attempts, k)
		} else {
			a.attempts[k] = fresh
		}
	}
	if len(a.attempts[ip]) >= 5 || len(a.attempts) > 1024 {
		a.mu.Unlock()
		fail(w, 429, "Изчакай една минута преди нов опит")
		return
	}
	a.attempts[ip] = append(a.attempts[ip], now)
	a.mu.Unlock()
	password, ok := body["password"].(string)
	if !ok {
		fail(w, 401, "Невалидна парола")
		return
	}
	candidate, e := pbkdf2.Key(sha256.New, password, a.salt, 210000, 32)
	if e != nil || !hmac.Equal(candidate, a.hash) {
		fail(w, 401, "Невалидна парола")
		return
	}
	id, csrf := token(), token()
	a.mu.Lock()
	if len(a.sessions) >= 128 {
		a.mu.Unlock()
		fail(w, 429, "Твърде много активни сесии")
		return
	}
	a.sessions[id] = Session{csrf, now.Add(8 * time.Hour)}
	delete(a.attempts, ip)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "sp_session", Value: id, Path: "/", MaxAge: 28800, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secure})
	reply(w, 200, Row{"csrf": csrf})
}
func runMain() error {
	demo := cliFlag.Bool("demo", false, "Симулация без връзка с рутер")
	data := cliFlag.String("data", env("DATA_DIR", "/data"), "Постоянна директория")
	configPath := cliFlag.String("config", "/data/config.json", "Конфигурация")
	bind := cliFlag.String("bind", "0.0.0.0", "HTTP адрес")
	port := cliFlag.Int("port", 8088, "HTTP порт")
	probe := cliFlag.Bool("native-syntax-probe", false, "Print a parse-only RouterOS probe")
	cliFlag.Parse()
	if *probe {
		o := ScriptOptions{SSID: "Probe Network", Password: "probe-only-123", Seconds: 15, Rename: true, CloseAP: true, Static: true, NameStart: "-11", Gateway: "192.168.89.1", Prefix: "192.168.89.", IPStart: 34, Mask: "255.255.255.0", DNS: "192.168.89.1"}
		p, e := nativeProgram(o, "ShellyPlus1-441793ABCD01", "wifi2", "SPPROBEPARSE", 0, 34)
		if e != nil {
			return e
		}
		fmt.Println(":local parser [:parse " + rosString(p) + "]; :put \"SP-NATIVE-SYNTAX-OK\";")
		return nil
	}
	store, e := newStore(*data)
	if e != nil {
		return errors.New("Не може да се отвори директорията за данни")
	}
	var config Config
	var router RouterAPI
	password := os.Getenv("PANEL_PASSWORD")
	if *demo {
		config, router = demoRouter()
		config.Hosts = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)), net.JoinHostPort("localhost", strconv.Itoa(*port))}
		password = "demo-local"
	} else {
		b, e := os.ReadFile(*configPath)
		if e != nil {
			return errors.New("Липсва config.json")
		}
		if json.Unmarshal(b, &config) != nil {
			return errors.New("Невалиден config.json")
		}
		if len(password) < 5 {
			return errors.New("PANEL_PASSWORD трябва да има поне 5 знака")
		}
		router, e = newRouter(os.Getenv("ROUTER_URL"), os.Getenv("ROUTER_USER"), os.Getenv("ROUTER_PASSWORD"), os.Getenv("ROUTER_CA_FILE"))
		if e != nil {
			return e
		}
	}
	service := newService(router, store, config)
	service.demo = *demo
	if *demo {
		seedDemo(store)
	}
	if store.read().WPS != nil {
		if e := service.recoverWPS(); e != nil {
			log.Print("WPS възстановяването изисква проверка")
		}
	}
	if store.read().Recovery != nil {
		if service.recover() != nil {
			log.Print("Невъзстановена сесия: промените остават блокирани")
		}
	}
	cert, key := os.Getenv("PANEL_TLS_CERT"), os.Getenv("PANEL_TLS_KEY")
	if (cert == "") != (key == "") {
		return errors.New("Задай едновременно PANEL_TLS_CERT и PANEL_TLS_KEY")
	}
	app, e := newApp(service, password, *demo, cert != "")
	if e != nil {
		return e
	}
	server := &http.Server{Addr: net.JoinHostPort(*bind, strconv.Itoa(*port)), Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0)}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		<-signals
		service.cancel.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Shelly panel listening on %s; demo=%v", server.Addr, *demo)
	if cert != "" {
		e = server.ListenAndServeTLS(cert, key)
	} else {
		e = server.ListenAndServe()
	}
	if service.gate.TryLock() {
		_ = service.recoverWPS()
		_ = service.recover()
		service.gate.Unlock()
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func env(k, fallback string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return fallback
}
func main() {
	if e := runMain(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
