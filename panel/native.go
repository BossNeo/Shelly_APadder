package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This is a byte-for-byte copy of the user's original TheScript. The adapter
// retains its per-device loop; only the documented Chateau/transport fixes apply.
//
//go:embed scripts/TheScript.rsc
var originalScript string

type ScriptOptions struct {
	SSID      string `json:"ssid"`
	Password  string `json:"password"`
	Seconds   int    `json:"scan_seconds"`
	Rename    bool   `json:"rename"`
	CloseAP   bool   `json:"disable_ap"`
	Static    bool   `json:"static_ip"`
	NameStart string `json:"name_start"`
	Gateway   string `json:"gateway"`
	Prefix    string `json:"prefix"`
	IPStart   int    `json:"ip_start"`
	Mask      string `json:"mask"`
	DNS       string `json:"dns"`
}

var nameSequence = regexp.MustCompile(`^-?[0-9]{1,6}$`)

func (o ScriptOptions) sequence() (string, int, error) {
	raw := strings.TrimSpace(o.NameStart)
	if !nameSequence.MatchString(raw) {
		return "", 0, errors.New("Начален номер: например 11 или -11 (тирето е разделител)")
	}
	sep := ""
	if strings.HasPrefix(raw, "-") {
		sep = "-"
		raw = raw[1:]
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1 {
		return "", 0, errors.New("Началният номер трябва да е поне 1")
	}
	return sep, n, nil
}
func (o ScriptOptions) validate(count int) error {
	if len(o.SSID) < 1 || len(o.SSID) > 32 {
		return errors.New("SSID трябва да е от 1 до 32 UTF-8 байта")
	}
	if len(o.Password) < 8 || len(o.Password) > 63 {
		return errors.New("Wi-Fi паролата трябва да е 8–63 ASCII знака")
	}
	for _, c := range o.Password {
		if c < 32 || c > 126 {
			return errors.New("Wi-Fi паролата изисква печатни ASCII знаци")
		}
	}
	if o.Seconds < 1 || o.Seconds > 45 {
		return errors.New("Сканирането трябва да е между 1 и 45 секунди")
	}
	if o.Rename {
		if _, _, e := o.sequence(); e != nil {
			return e
		}
	}
	if !o.Static {
		return nil
	}
	if o.IPStart < 1 || o.IPStart > 254 || o.IPStart+count-1 > 254 {
		return errors.New("Недостатъчно статични адреси до .254")
	}
	ip := net.ParseIP(o.Prefix + strconv.Itoa(o.IPStart))
	last := net.ParseIP(o.Prefix + strconv.Itoa(o.IPStart+max(count, 1)-1))
	gw := net.ParseIP(o.Gateway)
	dns := net.ParseIP(o.DNS)
	maskIP := net.ParseIP(o.Mask)
	if !strings.HasSuffix(o.Prefix, ".") || strings.Count(o.Prefix, ".") != 3 || ip == nil || last == nil || gw == nil || dns == nil || maskIP == nil || ip.To4() == nil || gw.To4() == nil || dns.To4() == nil || maskIP.To4() == nil {
		return errors.New("Провери IPv4 префикса, gateway, маската и DNS")
	}
	if !regexp.MustCompile(`^(\d{1,3}\.){3}$`).MatchString(o.Prefix) {
		return errors.New("Префиксът трябва да е като 192.168.88.")
	}
	mask := net.IPMask(maskIP.To4())
	bits, total := mask.Size()
	if total != 32 || bits < 1 || bits > 30 {
		return errors.New("Невалидна IPv4 мрежова маска")
	}
	network := ip.Mask(mask)
	if !network.Equal(gw.Mask(mask)) || !network.Equal(last.Mask(mask)) {
		return errors.New("Gateway и адресите трябва да са в една подмрежа")
	}
	for _, v := range []net.IP{ip, last, gw} {
		v = v.To4()
		host := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
		hostMask := uint32(1)<<(32-bits) - 1
		if host&hostMask == 0 || host&hostMask == hostMask {
			return errors.New("Мрежов и broadcast адрес не могат да се задават")
		}
	}
	if ip.Equal(gw) || last.Equal(gw) {
		return errors.New("Диапазонът не трябва да включва gateway")
	}
	start4 := ip.To4()
	g4 := gw.To4()
	l4 := last.To4()
	if start4[0] == g4[0] && start4[1] == g4[1] && start4[2] == g4[2] && g4[3] >= start4[3] && g4[3] <= l4[3] {
		return errors.New("Диапазонът включва gateway")
	}
	return nil
}
func options(body Row) (ScriptOptions, error) {
	o := ScriptOptions{Seconds: 15, NameStart: "-11", IPStart: 34, Gateway: "192.168.88.1", Prefix: "192.168.88.", Mask: "255.255.255.0", DNS: "8.8.8.8"}
	b, e := json.Marshal(body)
	if e == nil {
		e = json.Unmarshal(b, &o)
	}
	if e != nil {
		return o, errors.New("Невалидни настройки на скрипта")
	}
	return o, o.validate(0)
}
func (o ScriptOptions) deviceName(ssid string, index int) string {
	sep, n, _ := o.sequence()
	model := strings.TrimPrefix(ssid, ssid[:min(6, len(ssid))])
	if end := strings.Index(model, "-"); end >= 0 {
		model = model[:end]
	}
	return model + sep + strconv.Itoa(n+index)
}
func nativeFetch(env, phase, method string, params Row) string {
	b, _ := json.Marshal(Row{"id": 1, "method": method, "params": params})
	return ":set " + env + " ($" + env + " . " + rosString("TRY_"+phase+"\n") + "); " + fmt.Sprintf(":local r%s [/tool/fetch url=\"http://192.168.33.1/rpc\" http-method=post http-header-field=\"Content-Type:application/json\" http-data=%s output=user as-value duration=8s]; :local a%s [:deserialize from=json value=($r%s->\"data\")]; :if ([:typeof ($a%s->\"error\")] != \"nil\") do={:error \"Shelly RPC rejected\"}; :set %s ($%s . %s);", phase, rosString(string(b)), phase, phase, phase, env, env, rosString(phase+"\n"))
}
func nativeProgram(o ScriptOptions, ssid, radio, env string, index, ipNumber int) (string, error) {
	if !shellySSID.MatchString(ssid) || !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(env) {
		return "", errors.New("Невалиден Shelly или native token")
	}
	source := strings.ReplaceAll(originalScript, "\r", "")
	a := strings.Index(source, ":foreach j in=$ssids do={")
	b := strings.Index(source, ":put \"Sucseed")
	if a < 0 || b < a {
		return "", errors.New("Оригиналният TheScript е променен: необходим е преглед на адаптера")
	}
	sta := Row{"ssid": o.SSID, "pass": o.Password, "enable": true, "ipv4mode": "dhcp"}
	static := clone(sta)
	static["ipv4mode"] = "static"
	static["ip"] = o.Prefix + strconv.Itoa(ipNumber)
	static["gw"] = o.Gateway
	static["netmask"] = o.Mask
	static["nameserver"] = o.DNS
	lines := []string{":global " + env + " \"\";", ":do {", ":local ssids {" + rosString(ssid) + "};", ":local count 0; :local totalssids 1; :local badCount 0;", fmt.Sprintf(":local ChangeName %d; :local TurnOffAP %d; :local EnableStaticIP %d; :local StaticIPNUM %d;", boolInt(o.Rename), boolInt(o.CloseAP), boolInt(o.Static), ipNumber)}
	for _, line := range strings.Split(source[a:b], "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.Contains(trim, ":local badCount") {
			continue
		}
		if strings.Contains(line, "/tool/fetch") {
			phase, method, params := "", "", Row{}
			switch {
			case strings.Contains(line, "Sys.SetConfig"):
				phase = "NAME"
				method = "Sys.SetConfig"
				params = Row{"config": Row{"device": Row{"name": o.deviceName(ssid, index)}}}
			case strings.Contains(line, "Cloud.SetConfig"):
				phase = "CLOUD"
				method = "Cloud.SetConfig"
				params = Row{"config": Row{"enable": true}}
			case strings.Contains(line, "ipv4mode"):
				phase = "STA"
				method = "WiFi.SetConfig"
				params = Row{"config": Row{"sta": static}}
			case strings.Contains(line, "\\\"sta\\\""):
				phase = "STA"
				method = "WiFi.SetConfig"
				params = Row{"config": Row{"sta": sta}}
			case strings.Contains(line, "\\\"ap\\\""):
				phase = "AP"
				method = "WiFi.SetConfig"
				params = Row{"config": Row{"ap": Row{"enable": false}}}
			default:
				return "", errors.New("Неразпознат RPC ред в TheScript")
			}
			lines = append(lines, nativeFetch(env, phase, method, params))
			continue
		}
		if strings.HasPrefix(trim, ":put") {
			phase := "PROGRESS"
			if strings.Contains(line, "connecting to") {
				phase = "CONNECT"
			}
			if strings.Contains(line, "time out") {
				phase = "TIMEOUT"
			}
			lines = append(lines, ":set "+env+" ($"+env+" . "+rosString(phase+"\n")+");")
			continue
		}
		line = strings.ReplaceAll(line, "/ip dhcp-client get wifi1 address", "/ip/dhcp-client/get [find where comment="+rosString(temporary)+"] address")
		line = strings.ReplaceAll(line, "wifi1", rosString(radio))
		line = strings.ReplaceAll(line, ":set $", ":set ")
		if strings.HasPrefix(strings.TrimSpace(line), "delay ") {
			line = ":delay " + strings.TrimSpace(line)[6:]
		}
		lines = append(lines, line)
	}
	lines = append(lines, "} on-error={ :set "+env+" ($"+env+" . \"ERROR\\n\"); };", ":set "+env+" ($"+env+" . \"DONE\\n\");")
	return strings.Join(lines, "\n"), nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Service) stopWorker(snap *Recovery) error {
	jobs, e := s.router.Rows("system/script/job", nil)
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if str(j[".id"]) != snap.Worker {
			continue
		}
		if len(snap.WorkerFingerprint) == 0 {
			return errors.New("Изчакай native скрипта да приключи; възстановяването е запазено")
		}
		for k, v := range snap.WorkerFingerprint {
			if str(j[k]) != str(v) {
				return errors.New("Native job ID е променен: необходим е преглед")
			}
		}
		if e = s.router.Remove("system/script/job", snap.Worker); e != nil {
			return errors.New("Native скриптът не е спрян; възстановяването остава блокирано")
		}
	}
	jobs, e = s.router.Rows("system/script/job", nil)
	if e != nil {
		return e
	}
	for _, j := range jobs {
		if str(j[".id"]) == snap.Worker {
			return errors.New("Native скриптът още работи")
		}
	}
	return nil
}
func (s *Service) nativeDevice(o ScriptOptions, device Row, index, ipNumber int, result Row) error {
	return s.station(func(r Row) error {
		ssid := str(device["ssid"])
		s.message(fmt.Sprintf("[%d] Свързване към %s", index+1, ssid))
		if e := s.router.Patch(wifi, str(r[".id"]), Row{"configuration.ssid": ssid}); e != nil {
			return e
		}
		if e := s.waitBound(str(r["name"])); e != nil {
			return e
		}
		info, e := s.router.RPC("192.168.33.1", "Shelly.GetDeviceInfo", nil)
		if e != nil {
			return e
		}
		gen, _ := strconv.Atoi(str(info["gen"]))
		if !strings.EqualFold(str(info["id"]), ssid) || gen < 2 || flag(info["auth_en"]) {
			return errors.New("Shelly идентичността/Gen2+ не е потвърдена или има парола за RPC")
		}
		env := "SP" + token()[:16]
		program, e := nativeProgram(o, ssid, s.config.Radio2G, env, index, ipNumber)
		if e != nil {
			return e
		}
		v, e := s.router.Call("POST", "execute", Row{"script": program})
		if e != nil {
			return e
		}
		jobs, e := rowsOf(v)
		if e != nil || len(jobs) == 0 || str(jobs[0]["ret"]) == "" {
			return errors.New("Липсва ID на native TheScript")
		}
		snap := s.store.read().Recovery
		snap.Worker = str(jobs[0]["ret"])
		s.pendingWorker = snap
		if e = s.store.change(func(d *Data) { d.Recovery = clone(snap) }); e != nil {
			return e
		}
		listed, e := s.router.Rows("system/script/job", nil)
		if e != nil {
			return e
		}
		for _, j := range listed {
			if str(j[".id"]) == snap.Worker && str(j["owner"]) != "" && str(j["started"]) != "" {
				snap.WorkerFingerprint = Row{"owner": j["owner"], "started": j["started"]}
			}
		}
		if e = s.store.change(func(d *Data) { d.Recovery = clone(snap) }); e != nil {
			return e
		}
		defer func() {
			rows, _ := s.router.Rows("system/script/environment", Row{".query": []string{"name=" + env}})
			for _, r := range rows {
				if str(r["name"]) == env {
					_ = s.router.Remove("system/script/environment", str(r[".id"]))
				}
			}
		}()
		seen := 0
		done, failed := false, false
		for {
			if e = s.window(); e != nil {
				return e
			}
			events, e := s.router.Rows("system/script/environment", Row{".query": []string{"name=" + env}, ".proplist": "name,value,.id"})
			if e != nil {
				return errors.New("Native журналът не е достъпен")
			}
			for _, event := range events {
				if str(event["name"]) != env {
					continue
				}
				raw := str(event["value"])
				if raw == "" {
					continue
				}
				if len(raw) > 8192 {
					return errors.New("Невалиден native журнал")
				}
				phases := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
				for ; seen < len(phases); seen++ {
					phase := phases[seen]
					switch phase {
					case "CONNECT":
						s.message("TheScript: свързан с " + ssid)
					case "NAME":
						result["name"] = o.deviceName(ssid, index)
						s.message("Име: " + str(result["name"]))
					case "CLOUD":
						s.message("Shelly Cloud е включен според оригиналния скрипт")
					case "STA":
						result["settings_sent"] = true
						if o.Static {
							result["ip"] = o.Prefix + strconv.Itoa(ipNumber)
						}
						s.event("ok", "Wi-Fi настройките за "+o.SSID+" са приети от "+ssid)
					case "AP":
						result["ap_disabled"] = true
						s.event("ok", "Shelly AP е изключен: "+ssid)
					case "ERROR", "TIMEOUT":
						failed = true
						s.event("error", "TheScript: грешка при "+ssid+" (подробности с пароли не се записват)")
					case "DONE":
						done = true
					case "TRY_NAME":
						s.message("TheScript: задаване на име")
					case "TRY_CLOUD":
						s.message("TheScript: Cloud.SetConfig")
					case "TRY_STA":
						s.message("TheScript: изпращане на SSID и IP настройки")
					case "TRY_AP":
						s.message("TheScript: изключване на Shelly AP")
					}
				}
			}
			if done {
				if failed {
					return errors.New("TheScript отчете грешка за устройството")
				}
				if result["settings_sent"] != true {
					return errors.New("Wi-Fi настройките не са потвърдени")
				}
				return nil
			}
			listed, e = s.router.Rows("system/script/job", nil)
			if e != nil {
				return e
			}
			exists := false
			for _, j := range listed {
				exists = exists || str(j[".id"]) == snap.Worker
			}
			if !exists {
				return errors.New("Native скриптът приключи без финален маркер")
			}
			s.sleep(time.Second)
		}
	})
}
func (s *Service) scriptScan(seconds int) error {
	return s.scanNetworks(seconds, true)
}
func (s *Service) scanNetworks(seconds int, shellyOnly bool) error {
	s.message(fmt.Sprintf("Сканиране %d сек. · временна пауза на 2.4 GHz", seconds))
	found := map[string]Row{}
	e := s.station(func(r Row) error {
		v, e := s.router.Call("POST", wifi+"/scan", Row{".id": r[".id"], "duration": fmt.Sprint(seconds, "s")})
		if e != nil {
			return e
		}
		rows, e := rowsOf(v)
		if e != nil {
			return e
		}
		for _, row := range rows {
			ssid := str(row["ssid"])
			security := strings.ToLower(str(row["security"]))
			if ssid != "" && (!shellyOnly || (shellySSID.MatchString(ssid) && (security == "none" || security == "open" || security == ""))) {
				signal := row["signal"]
				if signal == nil {
					signal = row["signal-strength"]
				}
				previous, exists := found[ssid]
				oldSignal, oldError := strconv.Atoi(str(previous["signal"]))
				newSignal, newError := strconv.Atoi(str(signal))
				if !exists || (newError == nil && (oldError != nil || newSignal > oldSignal)) {
					found[ssid] = Row{"ssid": ssid, "signal": str(signal), "security": security}
				}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	list := []Row{}
	for _, r := range found {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return str(list[i]["ssid"]) < str(list[j]["ssid"]) })
	s.status.Lock()
	s.job.Found = list
	s.status.Unlock()
	label := "Намерени Wi-Fi мрежи на 2.4 GHz"
	if shellyOnly {
		label = "Намерени отворени Shelly AP мрежи"
	}
	s.event("ok", fmt.Sprintf("%s: %d", label, len(list)))
	return nil
}
func (s *Service) startScript(body Row) (Row, error) {
	o, e := options(body)
	scanOnly := body["scan_only"] == true
	networkScan := scanOnly && body["networks_only"] == true
	if scanOnly {
		o.SSID = "scan"
		o.Password = "scan-only"
		o.Static = false
		o.Rename = false
		b, _ := json.Marshal(body)
		e = json.Unmarshal(b, &o)
		if e == nil {
			if o.Seconds < 1 || o.Seconds > 45 {
				e = errors.New("Сканиране: 1–45 секунди")
			}
		}
	}
	if e != nil {
		return nil, e
	}
	if !s.mutationLock() {
		return nil, errors.New("Изчакай активната операция")
	}
	ok := false
	defer func() {
		if !ok {
			s.gate.Unlock()
		}
	}()
	if e = s.writable(); e != nil {
		return nil, e
	}
	if _, _, _, e = s.topology(); e != nil {
		return nil, e
	}
	s.cancel.Store(false)
	s.status.Lock()
	kind, message := "script", "Подготовка на TheScript"
	if scanOnly {
		kind, message = "scan", "Сканиране за Shelly без настройване на устройства"
	}
	if networkScan {
		kind, message = "networks", "Сканиране за Wi-Fi имена на 2.4 GHz"
	}
	s.job = Job{State: "running", Kind: kind, Message: message, Found: []Row{}, Results: []Row{}}
	s.status.Unlock()
	s.event("info", message+" · постоянният 5 GHz достъп се запазва")
	ok = true
	go func() {
		defer s.gate.Unlock()
		var err error
		if networkScan {
			if s.demo {
				s.sleep(time.Second)
				s.status.Lock()
				s.job.Found = []Row{{"ssid": "Object WiFi", "signal": "-45", "security": "wpa2-psk"}, {"ssid": "Guest 2.4G", "signal": "-62", "security": "wpa2-psk"}}
				s.status.Unlock()
			} else {
				err = s.scanNetworks(o.Seconds, false)
			}
			s.finishScript(err)
			return
		}
		if s.demo {
			s.runScriptDemo(o, scanOnly)
			return
		}
		err = s.scriptScan(o.Seconds)
		if err == nil && !scanOnly {
			devices := s.jobView().Found
			err = o.validate(len(devices))
			ipNumber := o.IPStart
			if err == nil {
				for i, d := range devices {
					if s.cancel.Load() {
						break
					}
					result := Row{"ssid": d["ssid"], "state": "sent", "target": o.SSID}
					e := s.nativeDevice(o, d, i, ipNumber, result)
					if result["settings_sent"] == true && o.Static {
						ipNumber++
					}
					if e != nil {
						result["state"] = "error"
						result["message"] = e.Error()
						s.event("error", e.Error())
					} else {
						result["message"] = "Настройките са приети. Свързването към целевата мрежа не е проверено."
					}
					s.status.Lock()
					s.job.Results = append(s.job.Results, clone(result))
					s.status.Unlock()
					result["time"] = time.Now().Unix()
					if save := s.store.change(func(data *Data) {
						data.History = append(data.History, clone(result))
						if len(data.History) > 500 {
							data.History = data.History[len(data.History)-500:]
						}
					}); save != nil {
						err = save
						break
					}
					if s.store.read().Recovery != nil {
						err = errors.New("Възстановяването не е потвърдено; следващите устройства са спрени")
						break
					}
				}
			}
		}
		s.finishScript(err)
	}()
	return Row{"started": true}, nil
}
func (s *Service) finishScript(e error) {
	state, message := "done", "Готово. Предишните състояния на мрежите са възстановени."
	if e != nil {
		state = "error"
		message = e.Error()
	} else if s.cancel.Load() {
		state = "cancelled"
		message = "Операцията е спряна и мрежите са възстановени."
	} else {
		if job := s.jobView(); job.Kind == "scan" {
			message = fmt.Sprintf("Намерени отворени Shelly AP мрежи: %d. Няма настройвани устройства. Мрежите са възстановени.", len(job.Found))
		} else if job.Kind == "networks" {
			message = fmt.Sprintf("Намерени Wi-Fi мрежи на 2.4 GHz: %d. Избери SSID и въведи паролата ръчно. Мрежите са възстановени.", len(job.Found))
		}
		for _, r := range s.jobView().Results {
			if r["state"] == "error" {
				state = "error"
				message = "Партидата завърши с грешки. Провери резултатите и журнала."
				break
			}
		}
	}
	s.status.Lock()
	s.job.State = state
	s.job.Message = message
	s.status.Unlock()
	level := "ok"
	if state == "error" {
		level = "error"
	}
	s.event(level, message)
}
func (s *Service) runScriptDemo(o ScriptOptions, scanOnly bool) {
	s.message("Демонстрация: сканиране без достъп до истински рутер")
	s.sleep(time.Second)
	devices := []Row{{"ssid": "ShellyPlus1-441793ABCD01", "signal": "-46"}, {"ssid": "Shelly1MiniG3-543204ABCD02", "signal": "-58"}, {"ssid": "ShellyPlusPlugS-441793ABCD03", "signal": "-63"}}
	s.status.Lock()
	s.job.Found = devices
	s.status.Unlock()
	s.event("ok", "Демо: намерени 3 Shelly AP мрежи")
	if !scanOnly {
		for i, d := range devices {
			if s.cancel.Load() {
				break
			}
			s.message("Демо: настройване на " + str(d["ssid"]))
			s.sleep(time.Second)
			result := Row{"ssid": d["ssid"], "state": "demo", "message": "Само симулация; няма реални промени"}
			if o.Rename {
				result["name"] = o.deviceName(str(d["ssid"]), i)
			}
			if o.Static {
				result["ip"] = o.Prefix + strconv.Itoa(o.IPStart+i)
			}
			s.status.Lock()
			s.job.Results = append(s.job.Results, result)
			s.status.Unlock()
			s.event("ok", "Демо: "+str(result["name"])+" · "+str(result["ip"]))
		}
	}
	s.finishScript(nil)
}
