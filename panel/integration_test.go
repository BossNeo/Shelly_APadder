package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validOptions() ScriptOptions {
	return ScriptOptions{SSID: "Other site", Password: "p\"a$ss\\word", Seconds: 15, Rename: true, CloseAP: true, Static: true, NameStart: "-11", Gateway: "192.168.77.1", Prefix: "192.168.77.", IPStart: 34, Mask: "255.255.255.0", DNS: "1.1.1.1"}
}
func TestNativeOriginalCopyAndAdaptation(t *testing.T) {
	b, e := os.ReadFile("../TheScript")
	if e != nil || string(b) != originalScript {
		t.Fatal("Original script copy changed", e)
	}
	o := validOptions()
	program, e := nativeProgram(o, "ShellyPlus1-441793ABCD01", "wifi2", "SPTEST", 0, 34)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(program, ":foreach j in=$ssids do={") || !strings.Contains(program, ":local counter 0;") {
		t.Fatal("Original loop not retained")
	}
	for _, bad := range []string{"wifi1", ":set $", "/tool/fetch url=\"http://192.168.33.1/rpc/", "p\"a$ss\\word"} {
		if strings.Contains(program, bad) {
			t.Fatal("Unsafe/unadapted native source", bad)
		}
	}
	if strings.Count(program, "http-method=post") != 5 || !strings.Contains(program, ":deserialize from=json") {
		t.Fatal("RPC validation or original methods missing")
	}
	if !strings.Contains(program, rosString(`"ssid":"Other site"`)[1:len(rosString(`"ssid":"Other site"`))-1]) {
		t.Fatal("Target SSID not serialized safely")
	}
	if e = o.validate(3); e != nil {
		t.Fatal(e)
	}
	for i, want := range []string{"Plus1-11", "Plus1-12", "Plus1-13"} {
		if got := o.deviceName("ShellyPlus1-441793ABCD01", i); got != want {
			t.Fatal(got, want)
		}
	}
	o.NameStart = "11"
	if o.deviceName("ShellyPlus1-441793ABCD01", 1) != "Plus112" {
		t.Fatal("Positive sequence changed")
	}
}
func TestScriptValidationRejectsWrongStaticRanges(t *testing.T) {
	for _, mutate := range []func(*ScriptOptions){func(o *ScriptOptions) { o.IPStart = 253 }, func(o *ScriptOptions) { o.Gateway = "10.0.0.1" }, func(o *ScriptOptions) { o.Mask = "255.0.255.0" }, func(o *ScriptOptions) { o.IPStart = 1 }, func(o *ScriptOptions) { o.NameStart = "--11" }, func(o *ScriptOptions) { o.Seconds = 90 }, func(o *ScriptOptions) { o.Prefix = "::ffff:192.168.77." }} {
		o := validOptions()
		mutate(&o)
		if o.validate(3) == nil {
			t.Fatal("Accepted invalid settings", o)
		}
	}
	o := validOptions()
	o.Static = false
	o.Gateway = "unused"
	if e := o.validate(3); e != nil {
		t.Fatal("DHCP restricted by unused static settings", e)
	}
}

type nativeRouter struct {
	*fakeRouter
	environment string
	nativeID    string
	failRPC     bool
}

func (r *nativeRouter) Call(method, path string, body Row) (any, error) {
	if path == "execute" && strings.HasPrefix(str(body["script"]), ":global SP") {
		p := str(body["script"])
		r.environment = strings.Fields(p)[1]
		r.nativeID = "*N"
		r.tables["system/script/environment"] = []Row{{".id": "*ENV", "name": r.environment, "value": "CONNECT\nNAME\nCLOUD\nSTA\nAP\nDONE\n"}}
		if r.failRPC {
			r.tables["system/script/environment"][0]["value"] = "CONNECT\nERROR\nDONE\n"
		}
		r.tables["system/script/job"] = append(r.tables["system/script/job"], Row{".id": r.nativeID, "owner": "panel", "started": "native-test"})
		return Row{"ret": r.nativeID}, nil
	}
	return r.fakeRouter.Call(method, path, body)
}
func TestNativeDeviceUsesExternalNetworkAndAlwaysRestores(t *testing.T) {
	for _, failure := range []bool{false, true} {
		s, f := fixture(t)
		r := &nativeRouter{fakeRouter: f, failRPC: failure}
		s.router = r
		before := clone(f.tables[wifi])
		o := validOptions()
		result := Row{}
		e := s.nativeDevice(o, Row{"ssid": "ShellyPlus1-441793ABCD01"}, 0, 34, result)
		if (e != nil) != failure {
			t.Fatal(e, result)
		}
		if s.store.read().Recovery != nil {
			t.Fatal("Recovery not cleared")
		}
		for i, row := range f.tables[wifi] {
			if str(row["name"]) == "wifi1" && str(row["configuration.ssid"]) != str(before[i]["configuration.ssid"]) {
				t.Fatal("Management changed")
			}
		}
		if !failure && (result["name"] != "Plus1-11" || result["settings_sent"] != true || result["ip"] != "192.168.77.34" || result["ap_disabled"] != true) {
			t.Fatal(result)
		}
		for _, event := range s.journal() {
			if strings.Contains(event.Message, o.Password) {
				t.Fatal("Password in journal")
			}
		}
	}
}
func TestRecoveryRefusesUnidentifiedWorker(t *testing.T) {
	s, f := fixture(t)
	f.tables["system/script/job"] = []Row{{".id": "*N", "owner": "other", "started": "later"}}
	if s.stopWorker(&Recovery{Worker: "*N", WorkerFingerprint: Row{"owner": "panel", "started": "earlier"}}) == nil {
		t.Fatal("Killed reused job ID")
	}
	if len(f.tables["system/script/job"]) != 1 {
		t.Fatal("Unrelated job removed")
	}
}
func TestJournalBoundedAndTelemetryPausedDuringMutation(t *testing.T) {
	s, _ := fixture(t)
	for i := 0; i < 250; i++ {
		s.event("info", "test")
	}
	j := s.journal()
	if len(j) != 200 || j[0].ID != 51 {
		t.Fatal(len(j))
	}
	s.gate.Lock()
	data, e := s.telemetry()
	s.gate.Unlock()
	if e != nil || data["paused"] != true {
		t.Fatal(data, e)
	}
}
func TestScriptHTTPRequiresLoginAndExplicitAcknowledgement(t *testing.T) {
	s, _ := fixture(t)
	a, e := newApp(s, "test-password-long", false, false)
	if e != nil {
		t.Fatal(e)
	}
	host := "127.0.0.1:8088"
	origin := "http://" + host
	for _, path := range []string{"/api/script", "/api/password", "/api/wps", "/api/probe"} {
		if w := request(a, "POST", path, host, origin, `{}`, "", ""); w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
	if w := request(a, "GET", "/api/telemetry", host, "", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(a, "POST", "/api/login", host, origin, `{"password":"test-password-long"}`, "", "")
	var login Row
	_ = json.Unmarshal(w.Body.Bytes(), &login)
	cookie := w.Result().Cookies()[0].String()
	if w = request(a, "POST", "/api/script", host, origin, `{"scan_only":true,"scan_seconds":15}`, cookie, str(login["csrf"])); w.Code != 400 {
		t.Fatal("Missing 5GHz/LAN acknowledgement accepted", w.Code)
	}
}

type wpsRouter struct {
	*fakeRouter
	refuse bool
}

func (r *wpsRouter) Call(method, path string, body Row) (any, error) {
	if path == wifi+"/wps-push-button" {
		if r.refuse {
			return nil, errors.New("WPS unsupported")
		}
		return nil, nil
	}
	return r.fakeRouter.Call(method, path, body)
}
func TestWPSStopKeepsEnabledNetworkAndFailureRollsBack(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, f := fixture(t)
		s.router = &wpsRouter{fakeRouter: f, refuse: fail}
		_, e := s.startWPS("sp-demo1")
		if (e != nil) != fail {
			t.Fatal(e)
		}
		if !fail {
			if s.store.read().WPS == nil {
				t.Fatal("WPS recovery not saved")
			}
			if _, e = s.stopWPS(); e != nil {
				t.Fatal(e)
			}
		}
		if s.store.read().WPS != nil {
			t.Fatal("WPS journal not cleared")
		}
		target := byName(f.tables[wifi], "sp-demo1")
		if active(target) == fail {
			t.Fatal("Network state on stop/failure is wrong", target)
		}
		if str(target["security.wps"]) != "" {
			t.Fatal("WPS direct override was not removed")
		}
	}
}
func TestTelemetryClientCountsCachingAndCounterReset(t *testing.T) {
	s, f := fixture(t)
	f.tables[wifi+"/registration-table"] = []Row{{"interface": "wifi2", "authorized": "true"}, {"interface": "wifi2", "authorized": "false"}, {"interface": "wifi1", "authorized": "true"}}
	f.tables["interface"] = []Row{{"name": "ether1", "rx-byte": "1000", "tx-byte": "2000"}}
	out, e := s.telemetry()
	if e != nil || str(out["clients"].(map[string]any)["wifi2"]) != "1" {
		t.Fatal(out, e)
	}
	f.tables["interface"][0]["rx-byte"] = "2000"
	s.telemetryAt = time.Time{}
	sample := s.trafficSamples["ether1"]
	sample.At = sample.At.Add(-5 * time.Second)
	s.trafficSamples["ether1"] = sample
	out, e = s.telemetry()
	if e != nil || out["traffic"].(map[string]any)["ether1"].(map[string]any)["available"] != true {
		t.Fatal(out, e)
	}
	f.tables["interface"][0]["rx-byte"] = "1"
	s.telemetryAt = time.Time{}
	out, e = s.telemetry()
	if e != nil || out["traffic"].(map[string]any)["ether1"].(map[string]any)["available"] != false {
		t.Fatal("Counter reset produced a false rate", out, e)
	}
}
func TestExternalTargetAcceptedWithoutLocalProjectOrEnabledSSID(t *testing.T) {
	s, _ := fixture(t)
	s.demo = true
	o := validOptions()
	raw, _ := json.Marshal(o)
	var body Row
	_ = json.Unmarshal(raw, &body)
	if _, e := s.startScript(body); e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(2 * time.Second)
	for s.jobView().State == "running" && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if s.jobView().State != "done" || len(s.jobView().Results) != 3 {
		t.Fatal(s.jobView())
	}
}
func TestScanOnlyNeedsNoTargetAndDoesNotConfigureDevices(t *testing.T) {
	s, f := fixture(t)
	before := make([]Row, len(f.tables[wifi]))
	for i, row := range f.tables[wifi] {
		before[i] = clone(row)
	}
	f.scanRows = []Row{
		{"ssid": "ShellyPlus1-441793ABCD01", "security": "None", "signal": "-50"},
		{"ssid": "ShellyPlus1-441793ABCD01", "security": "None", "signal": "-51"},
		{"ssid": "Shelly1MiniG3-543204ABCD02", "security": "Open", "signal": "-60"},
		{"ssid": "ShellyPlus1-441793ABCD03", "security": "WPA2"},
		{"ssid": "Other-SSID", "security": "None"},
	}
	if _, e := s.startScript(Row{"scan_only": true, "scan_seconds": 15}); e != nil {
		t.Fatal("Scan required target credentials", e)
	}
	until := time.Now().Add(2 * time.Second)
	for s.jobView().State == "running" && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	job := s.jobView()
	if job.State != "done" || job.Kind != "scan" || len(job.Found) != 2 || len(job.Results) != 0 || !strings.Contains(job.Message, ": 2.") {
		t.Fatal(job)
	}
	if len(f.rpcCalls) != 0 || len(s.store.read().History) != 0 || s.store.read().Recovery != nil || !reflect.DeepEqual(before, f.tables[wifi]) {
		t.Fatal("Scan configured a device or did not restore Wi-Fi")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(str(c.body["script"]), ":global SP") {
			t.Fatal("Native provisioning worker launched during scan")
		}
	}
}
func TestWPSSensitiveReadDoesNotAllowManagement(t *testing.T) {
	s, _ := fixture(t)
	if _, e := s.networkPassword("wifi1"); e == nil {
		t.Fatal("Read management password")
	}
	if _, e := s.startWPS("wifi1"); e == nil {
		t.Fatal("WPS on management")
	}
}

// Ensure a cancellation is handled before native code is launched.
func TestCancelledNativeDeviceRestores(t *testing.T) {
	s, f := fixture(t)
	s.cancel.Store(true)
	e := s.nativeDevice(validOptions(), Row{"ssid": "ShellyPlus1-441793ABCD01"}, 0, 34, Row{})
	if e == nil {
		t.Fatal("Cancellation ignored")
	}
	if s.store.read().Recovery != nil {
		t.Fatal("Snapshot lost on cancellation")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(str(c.body["script"]), ":global SP") {
			t.Fatal("Started after cancellation")
		}
	}
}
