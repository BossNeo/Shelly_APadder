package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// Exercise the production HTTP transport, not just the in-memory RouterAPI.
// RouterOS does not decode %2A in record IDs (documented REST API behaviour).
type restFaults struct {
	patchCalls, failPatchAt  int
	applyBeforeError, silent bool
}

func routerHTTPFixture(t *testing.T) (*Service, *fakeRouter, *restFaults) {
	t.Helper()
	s, f := fixture(t)
	faults := &restFaults{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(strings.ToUpper(req.RequestURI), "%2A") {
			http.Error(w, "missing or invalid resource identifier", 400)
			return
		}
		user, password, ok := req.BasicAuth()
		if !ok || user != "panel" || password != "router-password" {
			http.Error(w, "Unauthorized", 401)
			return
		}
		var body Row
		if req.Body != nil && req.Method != "DELETE" {
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", 400)
				return
			}
		}
		path := strings.TrimPrefix(req.URL.Path, "/rest/")
		var out any
		var err error
		switch req.Method {
		case "POST":
			if strings.HasSuffix(path, "/print") {
				out, err = f.Rows(strings.TrimSuffix(path, "/print"), body)
			} else if path == "tool/fetch" {
				var rpc struct {
					Method string
					Params Row
				}
				err = json.Unmarshal([]byte(str(body["http-data"])), &rpc)
				if err == nil {
					var u *url.URL
					u, err = url.Parse(str(body["url"]))
					if err == nil {
						var result Row
						result, err = f.RPC(u.Hostname(), rpc.Method, rpc.Params)
						data, _ := json.Marshal(Row{"id": 1, "result": result})
						out = []Row{{"status": "finished", "data": string(data)}}
					}
				}
			} else {
				out, err = f.Call("POST", path, body)
			}
		case "PATCH", "DELETE":
			end := strings.LastIndex(path, "/")
			menu, id := path[:end], path[end+1:]
			if req.Method == "DELETE" {
				err = f.Remove(menu, id)
			} else {
				faults.patchCalls++
				if faults.patchCalls == faults.failPatchAt {
					if faults.applyBeforeError {
						_ = f.Patch(menu, id, body)
					}
					if !faults.silent {
						err = fmt.Errorf("injected change refusal")
					}
				} else {
					err = f.Patch(menu, id, body)
				}
			}
		case "PUT":
			out, err = f.Add(path, body)
		default:
			err = fmt.Errorf("unexpected method %s", req.Method)
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(server.Close)
	router, err := newRouter(server.URL, "panel", "router-password", "")
	if err != nil {
		t.Fatal(err)
	}
	s.router = router
	return s, f, faults
}

func TestRESTCreateToggleScanAndRecoveryWithLiteralRecordIDs(t *testing.T) {
	s, f, _ := routerHTTPFixture(t)
	protected := copyRow(byName(f.tables[wifi], "wifi1"))
	if _, err := s.saveProject(Row{"title": "Site", "ssid": "New-Site", "password": "site-password"}); err != nil {
		t.Fatal(err)
	}
	p := s.store.read().Projects[0]
	for _, on := range []bool{true, false, true} {
		if _, err := s.switchNetworks([]string{p.Interface}, on); err != nil {
			t.Fatal(err)
		}
		if active(byName(f.tables[wifi], p.Interface)) != on {
			t.Fatal("network state did not change")
		}
	}
	wifiBefore := clone(f.tables[wifi])
	portsBefore := stripIDs(f.tables["interface/bridge/port"])
	if err := s.scan(); err != nil {
		t.Fatal(err)
	}
	if len(s.jobView().Found) != 1 || s.store.read().Recovery != nil {
		t.Fatal("scan or recovery incomplete")
	}
	if !reflect.DeepEqual(wifiBefore, f.tables[wifi]) || !reflect.DeepEqual(portsBefore, stripIDs(f.tables["interface/bridge/port"])) {
		t.Fatal("scan failed to restore network settings")
	}
	if !reflect.DeepEqual(protected, byName(f.tables[wifi], "wifi1")) {
		t.Fatal("protected 5 GHz configuration changed")
	}
	f.targetSSID = p.SSID
	f.tables["ip/dhcp-server/lease"] = []Row{{"mac-address": "44:17:93:AB:CD:01", "address": "192.168.89.100", "status": "bound"}}
	if err := s.provision(p, s.jobView().Found, Row{"rename": true, "disable_ap": true}); err != nil {
		t.Fatal(err)
	}
	results := s.jobView().Results
	if len(results) != 1 || results[0]["state"] != "verified" || results[0]["ap_disabled"] != true {
		t.Fatal("provisioning failed", results)
	}
	if !reflect.DeepEqual(wifiBefore, f.tables[wifi]) || !reflect.DeepEqual(protected, byName(f.tables[wifi], "wifi1")) {
		t.Fatal("provisioning did not restore Wi-Fi")
	}
}

func TestRESTRefusedTogglePreservesStateAndReportsCause(t *testing.T) {
	s, f, fail := routerHTTPFixture(t)
	before := clone(f.tables[wifi])
	fail.failPatchAt = 1
	_, err := s.switchNetworks([]string{"sp-demo1"}, true)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "Непълно") {
		t.Fatal("lost original cause or claimed incomplete rollback", err)
	}
	if !reflect.DeepEqual(before, f.tables[wifi]) {
		t.Fatal("refused toggle changed settings")
	}
}

func TestRESTPartialBulkFailureAndLostReplyRestoreStates(t *testing.T) {
	for _, mode := range []string{"refused", "reply-lost", "silent"} {
		t.Run(mode, func(t *testing.T) {
			s, f, faults := routerHTTPFixture(t)
			byName(f.tables[wifi], "sp-demo1")["disabled"] = "false"
			byName(f.tables[wifi], "sp-demo2")["disabled"] = "false"
			before := clone(f.tables[wifi])
			faults.failPatchAt = 2
			faults.applyBeforeError = mode == "reply-lost"
			faults.silent = mode == "silent"
			if _, err := s.switchNetworks([]string{"wifi2", "sp-demo1", "sp-demo2"}, false); err == nil {
				t.Fatal("reported failed write as success")
			}
			if !reflect.DeepEqual(before, f.tables[wifi]) {
				t.Fatal("partial bulk operation was not restored")
			}
		})
	}
}
