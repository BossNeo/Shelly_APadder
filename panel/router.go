package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Row map[string]any

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func flag(v any) bool   { s := strings.ToLower(str(v)); return s == "true" || s == "yes" }
func active(r Row) bool { return !flag(r["disabled"]) }
func copyRow(r Row) Row {
	b, _ := json.Marshal(r)
	var out Row
	_ = json.Unmarshal(b, &out)
	return out
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

type RouterAPI interface {
	Rows(string, Row) ([]Row, error)
	Call(string, string, Row) (any, error)
	Patch(string, string, Row) error
	Add(string, Row) (Row, error)
	Remove(string, string) error
	RPC(string, string, Row) (Row, error)
}
type Router struct {
	base, user, password string
	client               *http.Client
}

func newRouter(base, user, password, ca string) (*Router, error) {
	u, e := url.Parse(base)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Невалиден ROUTER_URL")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if ca != "" {
		pem, e := os.ReadFile(ca)
		if e != nil {
			return nil, e
		}
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("Невалиден CA")
		}
		t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Router{strings.TrimRight(base, "/") + "/rest/", user, password, &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (r *Router) Call(method, path string, body Row) (any, error) {
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, r.base+path, data)
	if e != nil {
		return nil, errors.New("Невалидна RouterOS заявка")
	}
	req.SetBasicAuth(r.user, r.password)
	req.Header.Set("Content-Type", "application/json")
	res, e := r.client.Do(req)
	if e != nil {
		return nil, errors.New("Няма връзка с RouterOS")
	}
	defer res.Body.Close()
	// Never return RouterOS error bodies: fetch errors may echo Wi-Fi passwords.
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("RouterOS %s %s: HTTP %d", method, path, res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 2_000_001))
	if e != nil || len(b) > 2_000_000 {
		return nil, errors.New("Невалиден размер на RouterOS отговора")
	}
	if len(b) == 0 {
		return nil, nil
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return nil, errors.New("Невалиден RouterOS JSON")
	}
	return out, nil
}
func rowsOf(v any) ([]Row, error) {
	if v == nil {
		return []Row{}, nil
	}
	if row, ok := v.(Row); ok {
		return []Row{row}, nil
	}
	if rows, ok := v.([]Row); ok {
		return rows, nil
	}
	if m, ok := v.(map[string]any); ok {
		return []Row{m}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("Невалиден RouterOS списък")
	}
	out := []Row{}
	for _, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("Невалиден RouterOS ред")
		}
		out = append(out, m)
	}
	return out, nil
}
func (r *Router) Rows(path string, params Row) ([]Row, error) {
	if params == nil {
		params = Row{}
	}
	v, e := r.Call("POST", path+"/print", params)
	if e != nil {
		return nil, e
	}
	return rowsOf(v)
}
func (r *Router) Patch(path, id string, data Row) error {
	if id == "" {
		return errors.New("Липсва RouterOS ID за промяна")
	}
	_, e := r.Call("PATCH", recordPath(path, id), data)
	return e
}
func (r *Router) Add(path string, data Row) (Row, error) {
	v, e := r.Call("PUT", path, data)
	if e != nil {
		return nil, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("Липсва ID на новия RouterOS запис")
	}
	return m, nil
}
func (r *Router) Remove(path, id string) error {
	if id == "" {
		return errors.New("Липсва RouterOS ID за премахване")
	}
	_, e := r.Call("DELETE", recordPath(path, id), nil)
	return e
}

func recordPath(path, id string) string {
	// RouterOS does not decode %2A in internal record IDs. Escape the rest of
	// the segment normally so names cannot inject a path or query string.
	if strings.HasPrefix(id, "*") {
		return path + "/*" + url.PathEscape(id[1:])
	}
	return path + "/" + url.PathEscape(id)
}
func (r *Router) RPC(address, method string, params Row) (Row, error) {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil {
		return nil, errors.New("Невалиден Shelly IP адрес")
	}
	if params == nil {
		params = Row{}
	}
	b, e := json.Marshal(Row{"id": 1, "method": method, "params": params})
	if e != nil {
		return nil, e
	}
	v, e := r.Call("POST", "tool/fetch", Row{"url": "http://" + ip.String() + "/rpc", "http-method": "post", "http-header-field": "Content-Type:application/json", "output": "user", "duration": "8s", "http-data": string(b)})
	if e != nil {
		return nil, e
	}
	rows, e := rowsOf(v)
	if e != nil {
		return nil, e
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if data, ok := rows[i]["data"]; ok {
			var reply struct {
				Result Row `json:"result"`
				Error  any `json:"error"`
			}
			if json.Unmarshal([]byte(str(data)), &reply) != nil || reply.Result == nil || reply.Error != nil {
				return nil, errors.New("Shelly отхвърли " + method)
			}
			return reply.Result, nil
		}
	}
	return nil, errors.New("Shelly не върна отговор")
}
