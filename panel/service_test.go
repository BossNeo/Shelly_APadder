package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type recorded struct {
	method, path string
	body         Row
}
type fakeRouter struct {
	*DemoRouter
	calls          []recorded
	failRecovery   bool
	silentRecovery bool
	scanRows       []Row
	targetSSID     string
	wrongID        bool
	auth           bool
	rpcCalls       []recorded
}

func fixture(t *testing.T) (*Service, *fakeRouter) {
	t.Helper()
	c, base := demoRouter()
	r := &fakeRouter{DemoRouter: base.(*DemoRouter), targetSSID: "Project-1", scanRows: []Row{{"ssid": "ShellyPlus1-441793ABCD01", "security": "None", "signal": "-50"}}}
	r.tables[wifi][1]["security.passphrase"] = "a$\"\\special"
	r.tables["interface/bridge/port"] = []Row{{".id": "*P", "interface": "wifi2", "bridge": "bridge", "disabled": "false", "dynamic": "false", "pvid": "17", "priority": "0x90", "ingress-filtering": "true"}, {".id": "*Q", "interface": "wifi1", "bridge": "bridge", "disabled": "false", "dynamic": "false"}}
	r.tables["ip/dhcp-client"] = []Row{}
	r.tables["ip/address"] = []Row{}
	r.tables["system/script/job"] = []Row{}
	r.tables["ip/dhcp-server/lease"] = []Row{}
	root, e := filepath.Abs(filepath.Join("..", "data", "go-tests"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	dir, e := os.MkdirTemp(root, "case-")
	if e != nil {
		t.Fatal(e)
	}
	rel, e := filepath.Rel(root, dir)
	if e != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		t.Fatal("test directory outside workspace")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, e := newStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	s := newService(r, store, c)
	s.sleep = func(time.Duration) {}
	return s, r
}
func (f *fakeRouter) Patch(path, id string, body Row) error {
	f.calls = append(f.calls, recorded{"PATCH", path, copyRow(body)})
	for k, v := range body {
		if b, ok := v.(bool); ok {
			body[k] = fmt.Sprint(b)
		}
	}
	return f.DemoRouter.Patch(path, id, body)
}
func (f *fakeRouter) Add(path string, body Row) (Row, error) {
	f.calls = append(f.calls, recorded{"PUT", path, copyRow(body)})
	if path == "ip/dhcp-client" {
		for _, p := range f.tables["interface/bridge/port"] {
			if p["interface"] == body["interface"] {
				return nil, errors.New("slave interface")
			}
		}
		body = copyRow(body)
		body["status"] = "bound"
		body["address"] = "192.168.33.2/24"
	}
	return f.DemoRouter.Add(path, body)
}
func (f *fakeRouter) Remove(path, id string) error {
	f.calls = append(f.calls, recorded{"DELETE", path, Row{".id": id}})
	return f.DemoRouter.Remove(path, id)
}
func unhex(s string) string {
	if !strings.HasPrefix(s, "\"") {
		return s
	}
	b, e := hex.DecodeString(strings.ReplaceAll(strings.Trim(s, "\""), "\\", ""))
	if e != nil {
		panic(e)
	}
	return string(b)
}

var pairsRE = regexp.MustCompile(`([a-z.\-]+)=("[^"]*"|yes|no)`)

func parsePairs(s string) Row {
	r := Row{}
	for _, p := range pairsRE.FindAllStringSubmatch(s, -1) {
		r[p[1]] = unhex(p[2])
	}
	return r
}

var unsetRE = regexp.MustCompile(`^/interface/wifi/unset value-name=([a-z.\-]+) \[/interface/wifi/find where name=("[^"]*")\]$`)
var setRE = regexp.MustCompile(`^/([a-z/]+)/set \[/[a-z/]+/find where (name|interface)=("[^"]*")\] (.*)$`)

func (f *fakeRouter) Call(method, path string, body Row) (any, error) {
	f.calls = append(f.calls, recorded{method, path, copyRow(body)})
	if path == wifi+"/scan" {
		b, _ := json.Marshal(f.scanRows)
		var out any
		_ = json.Unmarshal(b, &out)
		return out, nil
	}
	if path != "execute" {
		return nil, fmt.Errorf("unexpected %s", path)
	}
	script := str(body["script"])
	if _, ok := body["as-string"]; !ok {
		f.tables["system/script/job"] = append(f.tables["system/script/job"], Row{".id": "*J", "owner": "panel", "started": "test-start"})
		return Row{"ret": "*J"}, nil
	}
	if f.failRecovery && strings.Contains(script, "/ip/dhcp-client/remove") {
		return nil, errors.New("injected recovery failure")
	}
	if f.silentRecovery && strings.Contains(script, "/ip/dhcp-client/remove") {
		return nil, nil
	}
	for _, line := range strings.Split(script, ";\n") {
		if strings.HasPrefix(line, "/ip/dhcp-client/remove") {
			out := []Row{}
			for _, r := range f.tables["ip/dhcp-client"] {
				if str(r["comment"]) != temporary {
					out = append(out, r)
				}
			}
			f.tables["ip/dhcp-client"] = out
			continue
		}
		if m := unsetRE.FindStringSubmatch(line); m != nil {
			delete(byName(f.tables[wifi], unhex(m[2])), m[1])
			continue
		}
		if strings.HasPrefix(line, ":if ") {
			add := strings.SplitN(line, "/interface/bridge/port/add ", 2)[1]
			add = strings.SplitN(add, " } else=", 2)[0]
			props := parsePairs(add)
			found := false
			for _, p := range f.tables["interface/bridge/port"] {
				if p["interface"] == props["interface"] {
					for k, v := range props {
						p[k] = v
					}
					found = true
				}
			}
			if !found {
				_, _ = f.DemoRouter.Add("interface/bridge/port", props)
			}
			continue
		}
		if m := setRE.FindStringSubmatch(line); m != nil {
			found := false
			for _, r := range f.tables[m[1]] {
				if str(r[m[2]]) == unhex(m[3]) {
					for k, v := range parsePairs(m[4]) {
						r[k] = v
					}
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("missing restore target %s", m[1])
			}
			continue
		}
		return nil, fmt.Errorf("unrecognized script: %s", line)
	}
	return Row{"ret": ""}, nil
}
func (f *fakeRouter) RPC(address, method string, params Row) (Row, error) {
	f.rpcCalls = append(f.rpcCalls, recorded{address, method, clone(params)})
	switch method {
	case "Shelly.GetDeviceInfo":
		id := "shellyplus1-441793abcd01"
		if f.wrongID {
			id = "shellyplus1-441793abcd99"
		}
		return Row{"id": id, "gen": 2, "mac": "441793ABCD01", "auth_en": f.auth}, nil
	case "WiFi.GetStatus":
		return Row{"ssid": f.targetSSID, "sta_ip": address, "status": "got ip"}, nil
	}
	return Row{}, nil
}
func stripIDs(rows []Row) []Row {
	out := clone(rows)
	for _, r := range out {
		delete(r, ".id")
		delete(r, "dynamic")
	}
	sort.Slice(out, func(i, j int) bool { return str(out[i]["interface"]) < str(out[j]["interface"]) })
	return out
}
func TestStationRestoresAbsentFieldsAndBridgePort(t *testing.T) {
	s, r := fixture(t)
	before := clone(r.tables[wifi])
	ports := stripIDs(r.tables["interface/bridge/port"])
	injected := errors.New("test failure")
	e := s.station(func(radio Row) error {
		if str(byName(r.tables[wifi], "wifi2")["configuration.mode"]) != "station" {
			t.Fatal("station did not start")
		}
		for _, p := range r.tables["interface/bridge/port"] {
			if p["interface"] == "wifi2" {
				t.Fatal("slave bridge port was not detached")
			}
		}
		if s.store.read().Recovery == nil {
			t.Fatal("journal missing during mutation")
		}
		return injected
	})
	if !errors.Is(e, injected) {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, r.tables[wifi]) {
		t.Fatalf("wifi not restored\n%v\n%v", before, r.tables[wifi])
	}
	if !reflect.DeepEqual(ports, stripIDs(r.tables["interface/bridge/port"])) {
		t.Fatal("bridge configuration was not restored")
	}
	if s.store.read().Recovery != nil {
		t.Fatal("journal not cleared")
	}
	for _, c := range r.calls {
		if c.path == "execute" && strings.Contains(str(c.body["script"]), rosString("wifi1")) {
			t.Fatal("5 GHz referenced by recovery")
		}
	}
}
func TestRecoveryUsesUnsetRatherThanEmptyValues(t *testing.T) {
	s, _ := fixture(t)
	snap := &Recovery{Radio: RadioSnapshot{Name: "wifi2", Settings: Row{"configuration.mode": "ap"}, Disabled: false}, Keys: []string{"configuration.mode", "security", "security.passphrase"}}
	script := recoveryScript(snap)
	if !strings.Contains(script, "/unset value-name=security ") || strings.Contains(script, "security=\"\"") {
		t.Fatal(script)
	}
	_ = s
}
func TestFailedRecoveryBlocksFurtherChanges(t *testing.T) {
	s, r := fixture(t)
	r.failRecovery = true
	if s.station(func(Row) error { return nil }) == nil {
		t.Fatal("missing failure")
	}
	if s.store.read().Recovery == nil {
		t.Fatal("journal lost")
	}
	if _, e := s.switchNetworks([]string{"sp-demo1"}, false); e == nil {
		t.Fatal("pending recovery allowed write")
	}
	r.failRecovery = false
	if e := s.recover(); e != nil {
		t.Fatal(e)
	}
}
func TestRecoveryReadbackRejectsSilentFailure(t *testing.T) {
	s, r := fixture(t)
	r.silentRecovery = true
	if e := s.station(func(Row) error { return nil }); e == nil {
		t.Fatal("accepted silent recovery failure")
	}
	if s.store.read().Recovery == nil || len(r.tables["system/script/job"]) != 1 {
		t.Fatal("recovery safeguards were discarded")
	}
	r.silentRecovery = false
	if e := s.recover(); e != nil {
		t.Fatal(e)
	}
}
func TestRecoveryCannotTargetManagementOrAnotherRouter(t *testing.T) {
	for _, mode := range []string{"management", "wrong-board"} {
		t.Run(mode, func(t *testing.T) {
			s, r := fixture(t)
			snap := &Recovery{Radio: RadioSnapshot{Name: "wifi2", Settings: settings(r.tables[wifi][1])}}
			if mode == "management" {
				snap.Radio.Name = "wifi1"
			} else {
				s.config.ExpectedBoard = "Other"
			}
			if e := s.store.change(func(d *Data) { d.Recovery = snap }); e != nil {
				t.Fatal(e)
			}
			if e := s.recover(); e == nil {
				t.Fatal("unsafe recovery allowed")
			}
			if len(r.calls) > 0 || s.store.read().Recovery == nil {
				t.Fatal("unsafe recovery mutated or lost journal")
			}
		})
	}
}
func TestStationRefusesMutationIfJournalCannotBeSaved(t *testing.T) {
	s, r := fixture(t)
	s.store.path = filepath.Join(filepath.Dir(s.store.path), "missing", "state.json")
	if e := s.station(func(Row) error { t.Fatal("station started without durable journal"); return nil }); e == nil {
		t.Fatal("missing persistence failure")
	}
	if len(r.calls) > 0 {
		t.Fatal("router changed before saving recovery")
	}
}
func TestAutomaticBandsRequireMatchingSingleBandRadio(t *testing.T) {
	for _, mode := range []string{"valid", "swapped", "dual-band", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s, r := fixture(t)
			delete(r.tables[wifi][0], "channel.band")
			delete(r.tables[wifi][1], "channel.band")
			r.tables[wifi+"/radio"] = []Row{{"interface": "wifi1", "bands": "5ghz-a:20mhz,5ghz-ac:20mhz,5ghz-ax:20mhz"}, {"interface": "wifi2", "bands": "2ghz-n:20mhz,2ghz-ax:20mhz"}}
			if mode == "swapped" {
				r.tables[wifi+"/radio"][0]["interface"] = "wifi2"
				r.tables[wifi+"/radio"][1]["interface"] = "wifi1"
			}
			if mode == "dual-band" {
				r.tables[wifi+"/radio"][0]["bands"] = "2ghz-ax:20mhz,5ghz-ax:20mhz"
			}
			if mode == "missing" {
				r.tables[wifi+"/radio"] = nil
			}
			_, radio, _, e := s.topology()
			if mode == "valid" {
				if e != nil || str(radio["channel.band"]) != "2ghz-ax" {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("unverified hardware allowed")
			}
			if len(r.calls) > 0 {
				t.Fatal("inventory mutated router")
			}
		})
	}
}
func TestManagementAndMLDBlockedBeforeWrites(t *testing.T) {
	for _, mode := range []string{"protected", "mld", "model", "band", "captured-protected"} {
		t.Run(mode, func(t *testing.T) {
			s, r := fixture(t)
			names := []string{"sp-demo1"}
			switch mode {
			case "protected":
				names = []string{"wifi1"}
			case "mld":
				r.tables[wifi][1]["mld-interface"] = "mld1"
			case "model":
				s.config.ExpectedBoard = "Wrong"
			case "band":
				s.config.Radio2G = "wifi1"
			case "captured-protected":
				r.tables[wifi][0]["configuration.manager"] = "capsman"
			}
			if _, e := s.switchNetworks(names, false); e == nil {
				t.Fatal("unsafe write allowed")
			}
			if len(r.calls) > 0 {
				t.Fatal("mutated before validation")
			}
		})
	}
}
func TestBulkOrderAndReadOnly(t *testing.T) {
	s, r := fixture(t)
	if _, e := s.switchNetworks([]string{"sp-demo1", "wifi2"}, false); e != nil {
		t.Fatal(e)
	}
	if flag(byName(r.tables[wifi], "wifi1")["disabled"]) {
		t.Fatal("management disabled")
	}
	r.calls = nil
	if _, e := s.switchNetworks([]string{"sp-demo1", "wifi2"}, true); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(r.calls[0].body, Row{"disabled": false}) {
		t.Fatal("master not enabled first")
	}
	s.config.AllowChanges = false
	r.calls = nil
	if _, e := s.switchNetworks([]string{"wifi2"}, false); e == nil || len(r.calls) > 0 {
		t.Fatal("readonly mutated")
	}
}
func TestNewProjectStartsDisabledAndRedactsPassword(t *testing.T) {
	s, r := fixture(t)
	out, e := s.saveProject(Row{"title": "Обект", "ssid": "Site & test", "password": "a$\"\\special"})
	if e != nil {
		t.Fatal(e)
	}
	data := s.store.read()
	p := data.Projects[0]
	if !flag(byName(r.tables[wifi], p.Interface)["disabled"]) {
		t.Fatal("new network enabled automatically")
	}
	if out["id"] != p.ID {
		t.Fatal(out)
	}
	overview, e := s.overview()
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(overview)
	if strings.Contains(string(b), "password") || strings.Contains(string(b), "special") {
		t.Fatal("credential leaked")
	}
}
func TestScanFiltersFactoryAPs(t *testing.T) {
	s, r := fixture(t)
	r.scanRows = append(r.scanRows, r.scanRows[0], Row{"ssid": "CoffeeShop"}, Row{"ssid": "ShellyPlus1-441793ABCD02", "security": "WPA2"})
	if e := s.scan(); e != nil {
		t.Fatal(e)
	}
	if len(s.jobView().Found) != 1 {
		t.Fatal(s.jobView())
	}
}
func testProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := Project{ID: "p", Title: "Тест", SSID: "Project-1", Password: "test-pass", Interface: "sp-demo1", NextNumber: 1}
	if e := s.store.change(func(d *Data) { d.Projects = append(d.Projects, p) }); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestProvisionOnlyShutsAPAfterLANIdentityAndSSID(t *testing.T) {
	for _, mode := range []string{"verified", "wrong-ssid", "no-lease", "wrong-identity", "auth"} {
		t.Run(mode, func(t *testing.T) {
			s, r := fixture(t)
			p := testProject(t, s)
			byName(r.tables[wifi], p.Interface)["disabled"] = "false"
			r.tables["ip/dhcp-server/lease"] = []Row{{"mac-address": "44:17:93:AB:CD:01", "address": "192.168.89.100", "status": "bound"}}
			switch mode {
			case "wrong-ssid":
				r.targetSSID = "Other"
			case "no-lease":
				r.tables["ip/dhcp-server/lease"] = nil
			case "wrong-identity":
				r.wrongID = true
			case "auth":
				r.auth = true
			}
			if e := s.provision(p, r.scanRows, Row{"disable_ap": true}); e != nil {
				t.Fatal(e)
			}
			if len(r.rpcCalls) == 0 {
				t.Fatal("provision did not reach Shelly")
			}
			shutdown := false
			for _, c := range r.rpcCalls {
				if c.path == "Cloud.SetConfig" {
					t.Fatal("cloud modified")
				}
				if cfg, ok := c.body["config"].(map[string]any); ok {
					if _, ok := cfg["ap"]; ok {
						shutdown = true
						if c.method != "192.168.89.100" {
							t.Fatal("AP shut down via factory IP")
						}
					}
				}
			}
			if shutdown != (mode == "verified") {
				t.Fatalf("shutdown=%v mode=%s calls=%v", shutdown, mode, r.rpcCalls)
			}
		})
	}
}
func TestReusedWatchdogIDIsNotCancelled(t *testing.T) {
	s, r := fixture(t)
	snap := &Recovery{Radio: RadioSnapshot{Name: "wifi2", Settings: settings(r.tables[wifi][1]), Disabled: false}, Watchdog: "*J", Fingerprint: Row{"owner": "old", "started": "old"}}
	r.tables["system/script/job"] = []Row{{".id": "*J", "owner": "other", "started": "new"}}
	if e := s.store.change(func(d *Data) { d.Recovery = snap }); e != nil {
		t.Fatal(e)
	}
	if e := s.recover(); e != nil {
		t.Fatal(e)
	}
	if len(r.tables["system/script/job"]) != 1 {
		t.Fatal("unrelated job removed")
	}
}
func TestROSStringCannotInterpolate(t *testing.T) {
	input := "Тест\"; /system reboot; $password\\\n"
	encoded := rosString(input)
	if strings.Contains(encoded, "$") || strings.Contains(encoded, "reboot") || unhex(encoded) != input {
		t.Fatal(encoded)
	}
}
