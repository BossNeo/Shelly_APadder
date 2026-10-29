package main

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

type trafficSample struct {
	RX, TX uint64
	At     time.Time
}

func (s *Service) telemetry() (Row, error) {
	s.telemetryMu.Lock()
	defer s.telemetryMu.Unlock()
	if !s.telemetryAt.IsZero() && time.Since(s.telemetryAt) < 5*time.Second {
		return clone(s.telemetryData), nil
	}
	if !s.gate.TryLock() {
		return Row{"paused": true, "message": "Метриките са паузирани по време на операцията"}, nil
	}
	defer s.gate.Unlock()
	now := time.Now()
	out := Row{"time": now.Unix(), "clients": Row{}, "clients_available": false, "traffic": Row{}, "lte": Row{"status": "unknown"}, "internet": Row{"status": "unknown"}, "warnings": []string{}}
	if s.demo {
		out["demo"] = true
		out["clients_available"] = true
		out["clients"] = Row{"wifi2": 3}
		out["traffic"] = Row{"ether1": Row{"rx_bps": 12400000, "tx_bps": 1800000}, "wifi2": Row{"rx_bps": 300000, "tx_bps": 4100000}}
		out["lte"] = Row{"status": "demo", "rsrp": "-93dBm", "rsrq": "-10dB", "sinr": "12dB"}
		out["internet"] = Row{"status": "demo"}
		s.telemetryAt = now
		s.telemetryData = out
		return clone(out), nil
	}
	warnings := []string{}
	clients := Row{}
	if rows, e := s.router.Rows(wifi+"/registration-table", Row{".proplist": "interface,authorized"}); e == nil {
		for _, r := range rows {
			if v, ok := r["authorized"]; ok && !flag(v) {
				continue
			}
			name := str(r["interface"])
			n, _ := clients[name].(int)
			clients[name] = n + 1
		}
		out["clients"] = clients
		out["clients_available"] = true
	} else {
		warnings = append(warnings, "Броят Wi-Fi клиенти не е достъпен")
	}
	if s.trafficSamples == nil {
		s.trafficSamples = map[string]trafficSample{}
	}
	traffic := Row{}
	if rows, e := s.router.Rows("interface", Row{"stats": "", ".proplist": "name,type,running,rx-byte,tx-byte"}); e == nil {
		for _, r := range rows {
			name := str(r["name"])
			rx, er := strconv.ParseUint(str(r["rx-byte"]), 10, 64)
			tx, et := strconv.ParseUint(str(r["tx-byte"]), 10, 64)
			if er != nil || et != nil {
				continue
			}
			old, ok := s.trafficSamples[name]
			sample := Row{"available": false}
			if ok && rx >= old.RX && tx >= old.TX {
				sec := now.Sub(old.At).Seconds()
				if sec > 0 {
					sample = Row{"available": true, "rx_bps": float64(rx-old.RX) * 8 / sec, "tx_bps": float64(tx-old.TX) * 8 / sec}
				}
			}
			traffic[name] = sample
			s.trafficSamples[name] = trafficSample{rx, tx, now}
		}
		out["traffic"] = traffic
	} else {
		warnings = append(warnings, "Броячите за трафик не са достъпни")
	}
	if rows, e := s.router.Rows("interface/lte", Row{".proplist": ".id,name,running,disabled"}); e == nil {
		if len(rows) == 0 {
			out["lte"] = Row{"status": "absent"}
		} else {
			lte := rows[0]
			sample := Row{"status": "inactive", "interface": lte["name"]}
			if !flag(lte["disabled"]) {
				if v, e := s.router.Call("POST", "interface/lte/monitor", Row{".id": lte[".id"], "once": ""}); e == nil {
					if r, e := rowsOf(v); e == nil && len(r) > 0 {
						sample["status"] = str(r[0]["status"])
						for _, k := range []string{"rsrp", "rsrq", "rssi", "sinr", "current-operator", "data-class"} {
							if value, ok := r[0][k]; ok {
								sample[k] = value
							}
						}
					}
				}
			}
			out["lte"] = sample
		}
	}
	if s.probeAt.IsZero() || now.Sub(s.probeAt) > 30*time.Second {
		s.probeAt = now
		s.probeStatus = s.routerProbe()
	}
	out["internet"] = s.probeStatus
	out["warnings"] = warnings
	s.telemetryData = out
	s.telemetryAt = now
	return clone(out), nil
}
func (s *Service) routerProbe() Row {
	result := Row{"status": "unknown", "checked_at": time.Now().Unix(), "scope": "router", "detail": "Проверка от рутера; не доказва клиентски достъп през конкретен SSID"}
	v, e := s.router.Call("POST", "ping", Row{"address": "1.1.1.1", "count": "1", "interval": "1s"})
	if e != nil {
		return result
	}
	rows, e := rowsOf(v)
	if e != nil {
		return result
	}
	result["status"] = "no_reply"
	for _, r := range rows {
		if str(r["time"]) != "" && str(r["status"]) == "" {
			result["status"] = "reachable"
		}
		if n, _ := strconv.Atoi(str(r["received"])); n > 0 {
			result["status"] = "reachable"
		}
	}
	return result
}
func (s *Service) probeNetwork(name string) (Row, error) {
	if !s.gate.TryLock() {
		return nil, errors.New("Изчакай активната операция")
	}
	defer s.gate.Unlock()
	rows, radio, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	n := byName(rows, name)
	if n == nil || !s.managed(n, radio) || !active(n) || !active(radio) {
		return nil, errors.New("Мрежата е изключена или не е разрешена")
	}
	result := Row{"status": "unverified", "scope": "router-only", "interface": name, "detail": "Клиентският достъп през SSID не е проверен; тестът е от рутера"}
	if s.demo {
		result["router_status"] = "demo"
	} else {
		probe := s.routerProbe()
		result["router_status"] = probe["status"]
	}
	s.event("info", "Проверка за "+str(n["configuration.ssid"])+": тест от рутера, клиентският достъп остава непроверен")
	return result, nil
}

func stateString(v any) string { return strings.TrimSpace(str(v)) }
