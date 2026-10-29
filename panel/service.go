package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const wifi = "interface/wifi"
const marker = "shelly-panel:"
const temporary = "shelly-panel-station"

func token() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func byName(rows []Row, name string) Row {
	for _, r := range rows {
		if str(r["name"]) == name {
			return r
		}
	}
	return nil
}
func rosString(s any) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(str(s)) {
		fmt.Fprintf(&b, "\\%02X", c)
	}
	b.WriteByte('"')
	return b.String()
}
func selectROS(path, key string, value any) string {
	return "[/" + path + "/find where " + key + "=" + rosString(value) + "]"
}
func settings(r Row) Row {
	out := Row{}
	for k, v := range r {
		for _, g := range []string{"configuration", "security", "datapath", "channel"} {
			if k == g || strings.HasPrefix(k, g+".") {
				out[k] = v
				break
			}
		}
	}
	return out
}
func assignment(r Row) string {
	keys := []string{}
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a := []string{}
	for _, k := range keys {
		a = append(a, k+"="+rosString(r[k]))
	}
	return strings.Join(a, " ")
}

// Only writable bridge port properties; runtime counters and flags cannot be re-added.
var portKeys = strings.Fields("interface bridge disabled comment priority edge point-to-point learn horizon hw auto-isolate restricted-role restricted-tcn pvid frame-types ingress-filtering unknown-unicast-flood unknown-multicast-flood broadcast-flood tag-stacking bpdu-guard trusted multicast-router fast-leave path-cost internal-path-cost mvrp-registrar-state mvrp-applicant-state")

func portSettings(row Row) Row {
	r := Row{}
	for _, k := range portKeys {
		if v, ok := row[k]; ok {
			r[k] = v
		}
	}
	if _, ok := r["disabled"]; !ok {
		r["disabled"] = "no"
	}
	return r
}

type Job struct {
	State   string `json:"state"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
	Results []Row  `json:"results"`
	Found   []Row  `json:"found"`
}
type Service struct {
	router   RouterAPI
	store    *Store
	config   Config
	gate     sync.Mutex
	status   sync.Mutex
	job      Job
	cancel   atomic.Bool
	sleep    func(time.Duration)
	deadline time.Time
	demo     bool
}

func newService(r RouterAPI, s *Store, c Config) *Service {
	return &Service{router: r, store: s, config: c, job: Job{State: "idle", Message: "Готов за работа", Results: []Row{}, Found: []Row{}}, sleep: time.Sleep}
}
func (s *Service) jobView() Job     { s.status.Lock(); defer s.status.Unlock(); return clone(s.job) }
func (s *Service) message(m string) { s.status.Lock(); s.job.Message = m; s.status.Unlock() }
func (s *Service) writable() error {
	if !s.config.AllowChanges {
		return errors.New("Панелът е само за преглед. Завърши проверката и задай allow_changes=true.")
	}
	if s.store.read().Recovery != nil {
		return errors.New("Има невъзстановена сесия. Първо използвай Възстанови рутера.")
	}
	return nil
}
func (s *Service) topology() ([]Row, Row, Row, error) {
	res, e := s.router.Rows("system/resource", nil)
	if e != nil {
		return nil, nil, nil, e
	}
	rows, e := s.router.Rows(wifi, Row{"detail": ""})
	if e != nil {
		return nil, nil, nil, e
	}
	if e = s.resolveBands(rows); e != nil {
		return nil, nil, nil, e
	}
	return s.validateTopology(res, rows)
}

// RouterOS omits channel.band when automatic. Infer only from a matching
// single-band physical radio's advertised capabilities, never its SSID/name.
func (s *Service) resolveBands(rows []Row) error {
	needed := []Row{}
	for _, name := range []string{s.config.Radio2G, s.config.Protected5G} {
		if r := byName(rows, name); r != nil && str(r["channel.band"]) == "" {
			needed = append(needed, r)
		}
	}
	if len(needed) == 0 {
		return nil
	}
	caps, e := s.router.Rows(wifi+"/radio", Row{"detail": ""})
	if e != nil {
		return e
	}
	for _, r := range needed {
		for _, c := range caps {
			if str(c["interface"]) != str(r["name"]) && (str(r["radio-mac"]) == "" || !strings.EqualFold(str(c["radio-mac"]), str(r["radio-mac"]))) {
				continue
			}
			if str(c["radio-mac"]) != "" && str(r["radio-mac"]) != "" && !strings.EqualFold(str(c["radio-mac"]), str(r["radio-mac"])) {
				continue
			}
			bands := str(c["bands"])
			families := []string{}
			for _, f := range []string{"2ghz", "5ghz", "6ghz"} {
				if strings.Contains(bands, f+"-") {
					families = append(families, f)
				}
			}
			if len(families) != 1 {
				continue
			}
			for _, standard := range []string{"be", "ax", "ac", "n", "a", "g"} {
				band := families[0] + "-" + standard
				if strings.Contains(bands, band+":") {
					r["channel.band"] = band
					break
				}
			}
		}
	}
	return nil
}
func (s *Service) validateTopology(res, rows []Row) ([]Row, Row, Row, error) {
	if len(res) != 1 || s.config.ExpectedBoard == "" || str(res[0]["board-name"]) != s.config.ExpectedBoard {
		return nil, nil, nil, errors.New("Потвърди точния expected_board в config.json")
	}
	r, p := byName(rows, s.config.Radio2G), byName(rows, s.config.Protected5G)
	fail := func(m string) ([]Row, Row, Row, error) { return nil, nil, nil, errors.New(m) }
	if r == nil || p == nil || s.config.Radio2G == s.config.Protected5G {
		return fail("Задай различни физически radio_2g и protected_5g")
	}
	for _, v := range []Row{r, p} {
		if m := str(v["mld-interface"]); m != "" && m != "none" {
			return fail("MLD/MLO радио не се променя от панела")
		}
		if str(v["master-interface"]) != "" {
			return fail("Избери физическите радиа")
		}
		if m := str(v["configuration.manager"]); m != "" && m != "local" {
			return fail("CAPsMAN радио не се променя от панела")
		}
	}
	if !strings.HasPrefix(str(r["channel.band"]), "2ghz") || !strings.HasPrefix(str(p["channel.band"]), "5ghz") {
		return fail("Потвърди 2.4 и 5 GHz радиата чрез channel.band")
	}
	if !active(p) || (str(p["configuration.mode"]) != "" && str(p["configuration.mode"]) != "ap") {
		return fail("Защитеното 5 GHz радио трябва да е включено в AP режим")
	}
	return rows, r, p, nil
}
func (s *Service) managed(r, radio Row) bool {
	return str(r["name"]) == str(radio["name"]) || (str(r["master-interface"]) == str(radio["name"]) && (strings.HasPrefix(str(r["comment"]), marker) || contains(s.config.Existing, str(r["name"]))))
}
func (s *Service) overview() (Row, error) {
	rows, e := s.router.Rows(wifi, Row{"detail": ""})
	if e != nil {
		return nil, e
	}
	res, e := s.router.Rows("system/resource", nil)
	if e != nil {
		return nil, e
	}
	if len(res) == 0 {
		return nil, errors.New("Липсва system/resource")
	}
	te := s.resolveBands(rows)
	if te == nil {
		_, _, _, te = s.validateTopology(res, rows)
	}
	warning := ""
	if te != nil {
		warning = te.Error()
	}
	resource := Row{}
	for _, k := range []string{"board-name", "version", "free-memory", "free-hdd-space", "cpu-load", "uptime"} {
		resource[k] = res[0][k]
	}
	radio := byName(rows, s.config.Radio2G)
	if radio == nil {
		radio = Row{"name": "__unconfigured__"}
	}
	nets := []Row{}
	for _, r := range rows {
		nets = append(nets, Row{"name": r["name"], "ssid": str(r["configuration.ssid"]), "band": str(r["channel.band"]), "enabled": active(r), "virtual": str(r["master-interface"]) != "", "protected": str(r["name"]) == s.config.Protected5G, "managed": s.managed(r, radio)})
	}
	data := s.store.read()
	projects := []Row{}
	for _, p := range data.Projects {
		projects = append(projects, Row{"id": p.ID, "title": p.Title, "ssid": p.SSID, "interface": p.Interface})
	}
	history := data.History
	if len(history) > 100 {
		history = history[len(history)-100:]
	}
	return Row{"resource": resource, "interfaces": nets, "projects": projects, "history": history, "warning": warning, "allow_changes": s.config.AllowChanges, "recovery": data.Recovery != nil, "job": s.jobView()}, nil
}
func (s *Service) saveProject(body Row) (Row, error) {
	title, ssid, password, existing := strings.TrimSpace(str(body["title"])), str(body["ssid"]), str(body["password"]), str(body["existing_interface"])
	if title == "" || len([]rune(title)) > 80 || len(ssid) < 1 || len(ssid) > 32 {
		return nil, errors.New("Име до 80 знака и SSID до 32 UTF-8 байта са задължителни")
	}
	if len(password) < 8 || len(password) > 63 {
		return nil, errors.New("WPA2 паролата трябва да е 8–63 ASCII знака")
	}
	for _, c := range password {
		if c < 32 || c > 126 {
			return nil, errors.New("Използвай печатни ASCII знаци за WPA2 паролата")
		}
	}
	if !s.gate.TryLock() {
		return nil, errors.New("Изчакай текущата операция")
	}
	defer s.gate.Unlock()
	if e := s.writable(); e != nil {
		return nil, e
	}
	rows, r, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	p := Project{ID: token()[:12], Title: title, SSID: ssid, Password: password, NextNumber: 1}
	var created Row
	if existing != "" {
		target := byName(rows, existing)
		if target == nil || !s.managed(target, r) || str(target["configuration.ssid"]) != ssid {
			return nil, errors.New("Избери разрешена мрежа с този SSID")
		}
		for _, v := range s.store.read().Projects {
			if v.Interface == existing {
				return nil, errors.New("Мрежата вече има профил")
			}
		}
		p.Interface = existing
	} else {
		bridges, e := s.router.Rows("interface/bridge", nil)
		if e != nil {
			return nil, e
		}
		if byName(bridges, s.config.Bridge) == nil {
			return nil, errors.New("LAN bridge не съществува")
		}
		p.Interface = "sp-" + p.ID
		created, e = s.router.Add(wifi, Row{"name": p.Interface, "master-interface": r["name"], "disabled": "yes", "configuration.mode": "ap", "configuration.ssid": ssid, "security.authentication-types": "wpa2-psk", "security.encryption": "ccmp", "security.management-protection": "allowed", "security.passphrase": password, "security.wps": "disable", "datapath.bridge": s.config.Bridge, "comment": marker + p.ID})
		if e != nil {
			return nil, e
		}
	}
	if e = s.store.change(func(d *Data) { d.Projects = append(d.Projects, p) }); e != nil {
		if created != nil {
			if ce := s.router.Remove(wifi, str(created[".id"])); ce != nil {
				return nil, fmt.Errorf("Записът не успя; новият интерфейс %s изисква преглед", p.Interface)
			}
		}
		return nil, e
	}
	return Row{"id": p.ID}, nil
}
func (s *Service) switchNetworks(names []string, on bool) (Row, error) {
	if !s.gate.TryLock() {
		return nil, errors.New("Изчакай текущата операция")
	}
	defer s.gate.Unlock()
	if e := s.writable(); e != nil {
		return nil, e
	}
	rows, r, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	unique := map[string]bool{}
	for _, n := range names {
		unique[n] = true
	}
	targets := []Row{}
	for _, v := range rows {
		if unique[str(v["name"])] {
			if !s.managed(v, r) {
				return nil, errors.New("5 GHz и неразрешени интерфейси не се променят")
			}
			targets = append(targets, v)
		}
	}
	if len(targets) == 0 || len(targets) != len(unique) {
		return nil, errors.New("Избери валидни работни мрежи")
	}
	if on && (str(r["configuration.mode"]) != "ap" || (!active(r) && !unique[str(r["name"])])) {
		return nil, errors.New("Първо включи физическото 2.4 GHz радио в AP режим")
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if on {
			return str(targets[i]["name"]) == str(r["name"]) && str(targets[j]["name"]) != str(r["name"])
		}
		return str(targets[i]["name"]) != str(r["name"]) && str(targets[j]["name"]) == str(r["name"])
	})
	changed := []Row{}
	for _, v := range targets {
		if active(v) == on {
			continue
		}
		changed = append(changed, v)
		if e = s.router.Patch(wifi, str(v[".id"]), Row{"disabled": !on}); e != nil {
			return nil, s.toggleFailure(str(v["name"]), e, changed)
		}
	}
	wanted := []Row{}
	for _, v := range targets {
		wanted = append(wanted, Row{"name": v["name"], "disabled": !on})
	}
	if e = s.verifyNetworkStates(wanted); e != nil {
		return nil, s.toggleFailure("работните мрежи", e, changed)
	}
	return Row{"changed": len(changed)}, nil
}

func (s *Service) verifyNetworkStates(wanted []Row) error {
	rows, e := s.router.Rows(wifi, Row{"detail": ""})
	if e != nil {
		return e
	}
	for _, v := range wanted {
		actual := byName(rows, str(v["name"]))
		if actual == nil || active(actual) != active(v) {
			return fmt.Errorf("RouterOS не потвърди състоянието на %s", str(v["name"]))
		}
	}
	return nil
}

func (s *Service) toggleFailure(name string, cause error, before []Row) error {
	// The failed request may have changed nothing, or it may have been applied
	// before its reply was lost. Read back before deciding rollback is needed.
	if s.verifyNetworkStates(before) == nil {
		return fmt.Errorf("Промяната на %s не успя: %w. Предишните състояния са запазени.", name, cause)
	}
	for i := len(before) - 1; i >= 0; i-- {
		_ = s.router.Patch(wifi, str(before[i][".id"]), Row{"disabled": flag(before[i]["disabled"])})
	}
	if e := s.verifyNetworkStates(before); e != nil {
		return fmt.Errorf("Промяната на %s не успя: %w. Връщането не е потвърдено: %v", name, cause, e)
	}
	return fmt.Errorf("Промяната на %s не успя: %w. Предишните състояния са възстановени.", name, cause)
}

func recoveryScript(snap *Recovery) string {
	r := snap.Radio
	sel := selectROS(wifi, "name", r.Name)
	commands := []string{"/ip/dhcp-client/remove [find where comment=" + rosString(temporary) + "]", "/" + wifi + "/set " + sel + " disabled=yes"}
	for _, k := range snap.Keys {
		commands = append(commands, "/"+wifi+"/unset value-name="+k+" "+sel)
	}
	restored := copyRow(r.Settings)
	restored["disabled"] = "yes"
	commands = append(commands, "/"+wifi+"/set "+sel+" "+assignment(restored))
	for _, port := range snap.Ports {
		ps := selectROS("interface/bridge/port", "interface", port["interface"])
		if _, ok := port["bridge"]; !ok { // Older Python journals kept bridge membership intact.
			commands = append(commands, "/interface/bridge/port/set "+ps+" disabled="+rosString(port["disabled"]))
			continue
		}
		a := assignment(port)
		commands = append(commands, ":if ([:len "+ps+"] = 0) do={ /interface/bridge/port/add "+a+" } else={ /interface/bridge/port/set "+ps+" "+a+" }")
	}
	for _, child := range snap.Children {
		commands = append(commands, "/"+wifi+"/set "+selectROS(wifi, "name", child["name"])+" disabled="+rosString(child["disabled"]))
	}
	commands = append(commands, "/"+wifi+"/set "+sel+" disabled="+rosString(r.Disabled))
	return strings.Join(commands, ";\n")
}
func (s *Service) recover() error {
	snap := s.store.read().Recovery
	if snap == nil {
		return nil
	}
	if snap.Radio.Name != s.config.Radio2G || snap.Radio.Name == s.config.Protected5G {
		return errors.New("Recovery журналът е за друго радио: необходим е ръчен преглед")
	}
	res, e := s.router.Rows("system/resource", nil)
	if e != nil {
		return e
	}
	if len(res) != 1 || s.config.ExpectedBoard == "" || str(res[0]["board-name"]) != s.config.ExpectedBoard {
		return errors.New("Recovery журналът не може да се приложи на непотвърден рутер")
	}
	if _, e := s.router.Call("POST", "execute", Row{"script": recoveryScript(snap), "as-string": ""}); e != nil {
		return e
	}
	// A successful HTTP response alone does not prove the script restored all settings.
	// Keep both the journal and native watchdog until read-back confirms recovery.
	if e := s.verifyRecovery(snap); e != nil {
		return e
	}
	if snap.Watchdog != "" {
		jobs, e := s.router.Rows("system/script/job", nil)
		if e != nil {
			return e
		}
		for _, job := range jobs {
			if str(job[".id"]) == snap.Watchdog {
				if len(snap.Fingerprint) == 0 {
					return errors.New("Изчакай независимото възстановяване след 3 минути")
				}
				match := true
				for k, v := range snap.Fingerprint {
					match = match && str(job[k]) == str(v)
				}
				if match {
					if e = s.router.Remove("system/script/job", snap.Watchdog); e != nil {
						return e
					}
				}
			}
		}
	}
	return s.store.change(func(d *Data) { d.Recovery = nil })
}
func sameSetting(a, b any) bool {
	boolValue := func(v any) (bool, bool) {
		switch strings.ToLower(str(v)) {
		case "true", "yes":
			return true, true
		case "false", "no":
			return false, true
		}
		return false, false
	}
	av, ab := boolValue(a)
	bv, bb := boolValue(b)
	if ab && bb {
		return av == bv
	}
	return str(a) == str(b)
}
func (s *Service) verifyRecovery(snap *Recovery) error {
	failed := errors.New("Настройките след възстановяване не съвпадат: журналът и watchdog са запазени")
	rows, e := s.router.Rows(wifi, Row{"config": "", "show-sensitive": ""})
	if e != nil {
		return e
	}
	radio := byName(rows, snap.Radio.Name)
	if radio == nil || flag(radio["disabled"]) != flag(snap.Radio.Disabled) {
		return failed
	}
	for k, v := range snap.Radio.Settings {
		if !sameSetting(radio[k], v) {
			return failed
		}
	}
	for _, k := range snap.Keys {
		if _, existed := snap.Radio.Settings[k]; !existed && str(radio[k]) != "" {
			return failed
		}
	}
	for _, child := range snap.Children {
		r := byName(rows, str(child["name"]))
		if r == nil || flag(r["disabled"]) != flag(child["disabled"]) {
			return failed
		}
	}
	ports, e := s.router.Rows("interface/bridge/port", Row{"detail": ""})
	if e != nil {
		return e
	}
	for _, wanted := range snap.Ports {
		var actual Row
		for _, p := range ports {
			if str(p["interface"]) == str(wanted["interface"]) {
				actual = p
				break
			}
		}
		if actual == nil {
			return failed
		}
		for k, v := range wanted {
			if !sameSetting(actual[k], v) {
				return failed
			}
		}
	}
	clients, e := s.router.Rows("ip/dhcp-client", nil)
	if e != nil {
		return e
	}
	for _, c := range clients {
		if str(c["comment"]) == temporary {
			return failed
		}
	}
	return nil
}
func (s *Service) window() error {
	if time.Now().After(s.deadline) {
		return errors.New("Изтече времето на сесията: възстановяване на 2.4 GHz")
	}
	if s.cancel.Load() {
		return errors.New("Операцията е спряна")
	}
	return nil
}
func (s *Service) station(fn func(Row) error) (err error) {
	rows, radio, _, e := s.topology()
	if e != nil {
		return e
	}
	name := str(radio["name"])
	direct, e := s.router.Rows(wifi, Row{"config": "", "show-sensitive": ""})
	if e != nil {
		return e
	}
	d := byName(direct, name)
	if d == nil {
		return errors.New("Липсват директните настройки за възстановяване")
	}
	original := settings(d)
	for _, v := range original {
		if strings.Trim(str(v), "*") == "" && str(v) != "" {
			return errors.New("RouterOS не предоставя настройките за възстановяване: sensitive policy")
		}
	}
	for _, path := range []string{"ip/address", "ip/dhcp-client"} {
		rs, e := s.router.Rows(path, nil)
		if e != nil {
			return e
		}
		for _, v := range rs {
			if str(v["interface"]) == name {
				return errors.New("Радиото има директен IP или DHCP client: необходим е преглед")
			}
		}
	}
	country := str(radio["configuration.country"])
	patch := Row{"configuration.mode": "station", "configuration.ssid": "shelly-panel-idle", "channel.band": radio["channel.band"], "disabled": "no"}
	if country != "" {
		patch["configuration.country"] = country
	}
	keys := map[string]bool{}
	for k := range original {
		keys[k] = true
	}
	for k := range patch {
		if k != "disabled" {
			keys[k] = true
		}
	}
	snap := &Recovery{Radio: RadioSnapshot{Name: name, Settings: original, Disabled: flag(radio["disabled"])}, Keys: []string{}, Children: []Row{}, Ports: []Row{}}
	for k := range keys {
		snap.Keys = append(snap.Keys, k)
	}
	sort.Strings(snap.Keys)
	for _, child := range rows {
		if str(child["master-interface"]) == name {
			snap.Children = append(snap.Children, Row{"name": child["name"], "disabled": flag(child["disabled"])})
		}
	}
	ports, e := s.router.Rows("interface/bridge/port", Row{"detail": ""})
	if e != nil {
		return e
	}
	for _, p := range ports {
		if str(p["interface"]) == name && !flag(p["dynamic"]) {
			snap.Ports = append(snap.Ports, portSettings(p))
		}
	}
	if len(recoveryScript(snap)) > 60000 {
		return errors.New("Настройките са прекалено големи за независимото възстановяване")
	}
	if e = s.store.change(func(d *Data) { d.Recovery = clone(snap) }); e != nil {
		return e
	}
	defer func() {
		if re := s.recover(); re != nil {
			err = fmt.Errorf("Неуспешно възстановяване: %w", re)
		}
	}()
	s.deadline = time.Now().Add(100 * time.Second)
	v, e := s.router.Call("POST", "execute", Row{"script": ":delay 3m;\n" + recoveryScript(snap)})
	if e != nil {
		return e
	}
	wr, e := rowsOf(v)
	if e != nil || len(wr) == 0 {
		return errors.New("Липсва независим RouterOS watchdog")
	}
	snap.Watchdog = str(wr[0]["ret"])
	if snap.Watchdog == "" {
		return errors.New("Липсва ID на watchdog")
	}
	if e = s.store.change(func(d *Data) { d.Recovery = clone(snap) }); e != nil {
		return e
	}
	jobs, e := s.router.Rows("system/script/job", nil)
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if str(j[".id"]) == snap.Watchdog && str(j["owner"]) != "" && str(j["started"]) != "" {
			snap.Fingerprint = Row{"owner": j["owner"], "started": j["started"]}
		}
	}
	if len(snap.Fingerprint) == 0 {
		return errors.New("RouterOS не потвърди таймера за възстановяване")
	}
	if e = s.store.change(func(d *Data) { d.Recovery = clone(snap) }); e != nil {
		return e
	}
	for _, child := range snap.Children {
		if e = s.window(); e != nil {
			return e
		}
		if e = s.router.Patch(wifi, str(byName(rows, str(child["name"]))[".id"]), Row{"disabled": "yes"}); e != nil {
			return e
		}
	}
	for _, p := range ports {
		if str(p["interface"]) == name && !flag(p["dynamic"]) {
			if e = s.window(); e != nil {
				return e
			}
			if e = s.router.Remove("interface/bridge/port", str(p[".id"])); e != nil {
				return e
			}
		}
	}
	commands := []string{"/" + wifi + "/set " + selectROS(wifi, "name", name) + " disabled=yes"}
	for _, k := range snap.Keys {
		commands = append(commands, "/"+wifi+"/unset value-name="+k+" "+selectROS(wifi, "name", name))
	}
	commands = append(commands, "/"+wifi+"/set "+selectROS(wifi, "name", name)+" "+assignment(patch))
	if e = s.window(); e != nil {
		return e
	}
	if _, e = s.router.Call("POST", "execute", Row{"script": strings.Join(commands, ";\n"), "as-string": ""}); e != nil {
		return e
	}
	if e = s.window(); e != nil {
		return e
	}
	if _, e = s.router.Add("ip/dhcp-client", Row{"interface": name, "add-default-route": "no", "use-peer-dns": "no", "use-peer-ntp": "no", "disabled": "no", "comment": temporary}); e != nil {
		return e
	}
	if e = s.window(); e != nil {
		return e
	}
	return fn(radio)
}

var shellySSID = regexp.MustCompile(`(?i)^shelly[A-Za-z0-9_-]*-[A-Fa-f0-9]{12}$`)
var macChars = regexp.MustCompile(`[^a-f0-9]`)

func normalizedMAC(v any) string { return macChars.ReplaceAllString(strings.ToLower(str(v)), "") }
func (s *Service) start(kind string, body Row) (Row, error) {
	if kind != "scan" && kind != "provision" {
		return nil, errors.New("Невалидна операция")
	}
	if !s.gate.TryLock() {
		return nil, errors.New("Вече има активна операция")
	}
	ok := false
	defer func() {
		if !ok {
			s.gate.Unlock()
		}
	}()
	if e := s.writable(); e != nil {
		return nil, e
	}
	rows, radio, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	var project Project
	var devices []Row
	if kind == "provision" {
		for _, p := range s.store.read().Projects {
			if p.ID == str(body["project"]) {
				project = p
			}
		}
		requested, e := stringList(body["devices"])
		if e != nil {
			return nil, e
		}
		unique := map[string]bool{}
		for _, v := range requested {
			unique[v] = true
		}
		for _, v := range s.jobView().Found {
			if unique[str(v["ssid"])] {
				devices = append(devices, v)
			}
		}
		if project.ID == "" || len(devices) == 0 || len(unique) != len(requested) || len(devices) != len(unique) {
			return nil, errors.New("Избери обект и устройства от последното сканиране")
		}
		target := byName(rows, project.Interface)
		if target == nil || !s.managed(target, radio) || !active(target) || !active(radio) || str(radio["configuration.mode"]) != "ap" {
			return nil, errors.New("Първо включи целевата мрежа и физическото 2.4 GHz радио в AP режим")
		}
		if str(target["configuration.ssid"]) != project.SSID {
			return nil, errors.New("SSID е променен извън панела: обнови профила")
		}
	}
	s.cancel.Store(false)
	s.status.Lock()
	found := s.job.Found
	s.job = Job{State: "running", Kind: kind, Message: "Подготовка…", Results: []Row{}, Found: found}
	s.status.Unlock()
	ok = true
	if s.demo {
		go s.runDemo(kind, devices)
		return Row{"started": true}, nil
	}
	go func() {
		defer s.gate.Unlock()
		var e error
		if kind == "scan" {
			e = s.scan()
		} else {
			e = s.provision(project, devices, body)
		}
		s.status.Lock()
		defer s.status.Unlock()
		if e != nil {
			s.job.State = "error"
			s.job.Message = e.Error()
		} else if s.cancel.Load() {
			s.job.State = "cancelled"
			s.job.Message = "Операцията е спряна. Режимът е възстановен."
		} else {
			s.job.State = "done"
			s.job.Message = "Готово. Режимът на рутера е възстановен."
		}
	}()
	return Row{"started": true}, nil
}
func (s *Service) scan() error {
	s.message("Сканиране — 2.4 GHz мрежите временно прекъсват…")
	found := map[string]Row{}
	e := s.station(func(r Row) error {
		v, e := s.router.Call("POST", wifi+"/scan", Row{".id": r[".id"], "duration": "12s"})
		if e != nil {
			return e
		}
		rows, e := rowsOf(v)
		if e != nil {
			return e
		}
		for _, v := range rows {
			ssid, security := str(v["ssid"]), strings.ToLower(str(v["security"]))
			if shellySSID.MatchString(ssid) && (security == "none" || security == "open" || security == "") {
				signal := v["signal"]
				if signal == nil {
					signal = v["signal-strength"]
				}
				found[ssid] = Row{"ssid": ssid, "signal": str(signal)}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	list := []Row{}
	for _, v := range found {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return str(list[i]["ssid"]) < str(list[j]["ssid"]) })
	s.status.Lock()
	s.job.Found = list
	s.status.Unlock()
	return nil
}
func (s *Service) waitBound(name string) error {
	for i := 0; i < 30; i++ {
		if e := s.window(); e != nil {
			return e
		}
		rows, e := s.router.Rows("ip/dhcp-client", nil)
		if e != nil {
			return e
		}
		for _, v := range rows {
			if str(v["interface"]) == name && str(v["status"]) == "bound" && strings.HasPrefix(str(v["address"]), "192.168.33.") {
				return nil
			}
		}
		s.sleep(time.Second)
	}
	return errors.New("Shelly не даде DHCP адрес до 30 секунди")
}
func (s *Service) verify(info Row, ssid string) string {
	for i := 0; i < 12; i++ {
		if s.cancel.Load() {
			return ""
		}
		leases, e := s.router.Rows("ip/dhcp-server/lease", nil)
		if e != nil {
			return ""
		}
		for _, l := range leases {
			if normalizedMAC(l["mac-address"]) != normalizedMAC(info["mac"]) || str(l["status"]) != "bound" {
				continue
			}
			ip := net.ParseIP(str(l["address"]))
			if ip == nil || ip.To4() == nil {
				continue
			}
			address := ip.String()
			check, e := s.router.RPC(address, "Shelly.GetDeviceInfo", nil)
			if e != nil || str(check["id"]) != str(info["id"]) {
				continue
			}
			status, e := s.router.RPC(address, "WiFi.GetStatus", nil)
			if e == nil && str(status["ssid"]) == ssid && str(status["sta_ip"]) == address && (str(status["status"]) == "got ip" || str(status["status"]) == "connected") {
				return address
			}
		}
		s.sleep(2 * time.Second)
	}
	return ""
}
func (s *Service) provision(project Project, devices []Row, body Row) error {
	rows, radio, _, e := s.topology()
	if e != nil {
		return e
	}
	target := byName(rows, project.Interface)
	if target == nil || !active(target) || !active(radio) {
		return errors.New("Първо включи целевата мрежа и 2.4 GHz радиото")
	}
	if str(target["configuration.ssid"]) != project.SSID {
		return errors.New("SSID е променен извън панела: обнови профила")
	}
	if project.NextNumber < 1 {
		project.NextNumber = 1
	}
	for i, device := range devices {
		if s.cancel.Load() {
			break
		}
		s.message(fmt.Sprintf("%d/%d: %s", i+1, len(devices), str(device["ssid"])))
		result := Row{"ssid": device["ssid"], "state": "error"}
		var info Row
		e = s.station(func(r Row) error {
			if e := s.router.Patch(wifi, str(r[".id"]), Row{"configuration.ssid": device["ssid"]}); e != nil {
				return e
			}
			if e := s.waitBound(str(r["name"])); e != nil {
				return e
			}
			var e error
			info, e = s.router.RPC("192.168.33.1", "Shelly.GetDeviceInfo", nil)
			if e != nil {
				return e
			}
			generation, genError := strconv.Atoi(str(info["gen"]))
			if !strings.EqualFold(str(info["id"]), str(device["ssid"])) || genError != nil || generation < 2 || flag(info["auth_en"]) || len(normalizedMAC(info["mac"])) != 12 {
				return errors.New("Устройството не е избраният Shelly Gen2+ без автентикация")
			}
			result["device_id"] = info["id"]
			result["mac"] = info["mac"]
			if e = s.window(); e != nil {
				return e
			}
			if flag(body["rename"]) {
				if _, e = s.router.RPC("192.168.33.1", "Sys.SetConfig", Row{"config": Row{"device": Row{"name": fmt.Sprintf("%s %d", project.Title, project.NextNumber)}}}); e != nil {
					return e
				}
			}
			if e = s.window(); e != nil {
				return e
			}
			_, e = s.router.RPC("192.168.33.1", "WiFi.SetConfig", Row{"config": Row{"sta": Row{"ssid": project.SSID, "pass": project.Password, "enable": true, "ipv4mode": "dhcp"}}})
			return e
		})
		if s.store.read().Recovery != nil {
			return e
		}
		if e != nil {
			result["message"] = e.Error()
		} else {
			result["state"] = "pending"
			project.NextNumber++
			address := s.verify(info, project.SSID)
			if address != "" {
				result["state"] = "verified"
				result["ip"] = address
				if flag(body["disable_ap"]) {
					if _, e = s.router.RPC(address, "WiFi.SetConfig", Row{"config": Row{"ap": Row{"enable": false}}}); e != nil {
						result["message"] = "Свързването е потвърдено, но AP не е изключен"
					} else {
						result["ap_disabled"] = true
					}
				}
			} else {
				result["message"] = "Свързването не е потвърдено. Фабричният AP остава включен."
			}
		}
		s.status.Lock()
		s.job.Results = append(s.job.Results, copyRow(result))
		s.status.Unlock()
		result["time"] = time.Now().Unix()
		result["project"] = project.Title
		if e = s.store.change(func(d *Data) {
			d.History = append(d.History, result)
			if len(d.History) > 500 {
				d.History = d.History[len(d.History)-500:]
			}
			for n := range d.Projects {
				if d.Projects[n].ID == project.ID {
					d.Projects[n].NextNumber = project.NextNumber
				}
			}
		}); e != nil {
			return e
		}
	}
	return nil
}
func stringList(v any) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("Избери списък с устройства или интерфейси")
	}
	out := []string{}
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("Невалиден списък")
		}
		out = append(out, s)
	}
	return out, nil
}
