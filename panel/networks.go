package main

import (
	"errors"
	"strings"
	"time"
)

type WPSRecovery struct {
	Interface        string `json:"interface"`
	Direct           any    `json:"direct"`
	HadDirect        bool   `json:"had_direct"`
	Until            int64  `json:"until"`
	Radio            string `json:"radio"`
	WasDisabled      bool   `json:"was_disabled"`
	RadioWasDisabled bool   `json:"radio_was_disabled"`
	RestoreStates    bool   `json:"restore_states"`
}

func (s *Service) networkPassword(name string) (Row, error) {
	if !s.mutationLock() {
		return nil, errors.New("Изчакай текущата операция")
	}
	defer s.gate.Unlock()
	rows, radio, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	target := byName(rows, name)
	if target == nil || !s.managed(target, radio) {
		return nil, errors.New("Избери разрешена 2.4 GHz мрежа")
	}
	if s.demo {
		password := str(target["security.passphrase"])
		if password == "" {
			password = "demo-password"
		}
		return Row{"ssid": target["configuration.ssid"], "password": password}, nil
	}
	// Resolve effective security on the router, without copying other credentials
	// into the response or keeping a credential cache in the panel.
	v, e := s.router.Call("POST", "execute", Row{"script": ":put [/interface/wifi/get " + selectROS(wifi, "name", name) + " value-name=security.passphrase]", "as-string": ""})
	if e != nil {
		return nil, e
	}
	out, e := rowsOf(v)
	if e != nil || len(out) == 0 {
		return nil, errors.New("Паролата не може да бъде прочетена")
	}
	password := str(out[0]["ret"])
	password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	if password == "" || strings.Trim(password, "*") == "" {
		return nil, errors.New("Паролата е скрита или не е зададена. Провери sensitive policy и security профила")
	}
	return Row{"ssid": target["configuration.ssid"], "password": password}, nil
}
func (s *Service) startWPS(name string) (Row, error) {
	if !s.mutationLock() {
		return nil, errors.New("Изчакай активната операция")
	}
	defer s.gate.Unlock()
	if e := s.writable(); e != nil {
		return nil, e
	}
	rows, radio, _, e := s.topology()
	if e != nil {
		return nil, e
	}
	target := byName(rows, name)
	if target == nil || !s.managed(target, radio) {
		return nil, errors.New("WPS е разрешен само за работна 2.4 GHz мрежа")
	}
	direct, e := s.router.Rows(wifi, Row{"config": ""})
	if e != nil {
		return nil, e
	}
	d := byName(direct, name)
	if d == nil {
		return nil, errors.New("Липсва конфигурация на мрежата")
	}
	value, had := d["security.wps"]
	snap := &WPSRecovery{Interface: name, Direct: value, HadDirect: had, Until: time.Now().Add(120 * time.Second).Unix(), Radio: str(radio["name"]), WasDisabled: !active(target), RadioWasDisabled: !active(radio), RestoreStates: true}
	if e = s.store.change(func(d *Data) { d.WPS = clone(snap) }); e != nil {
		return nil, e
	}
	fail := func(cause error) (Row, error) { _ = s.recoverWPS(); return nil, cause }
	if s.demo {
		_ = s.router.Patch(wifi, str(radio[".id"]), Row{"disabled": false})
		_ = s.router.Patch(wifi, str(target[".id"]), Row{"disabled": false})
		snap.RestoreStates = false
		_ = s.store.change(func(d *Data) { d.WPS = clone(snap) })
		s.event("info", "Демо WPS: "+name)
		time.AfterFunc(120*time.Second, func() { s.expireWPS(snap.Until) })
		return Row{"until": snap.Until}, nil
	}
	if !active(radio) {
		if e = s.router.Patch(wifi, str(radio[".id"]), Row{"disabled": false}); e != nil {
			return fail(e)
		}
	}
	if !active(target) {
		if e = s.router.Patch(wifi, str(target[".id"]), Row{"disabled": false}); e != nil {
			return fail(e)
		}
	}
	if e = s.router.Patch(wifi, str(target[".id"]), Row{"security.wps": "push-button"}); e != nil {
		return fail(e)
	}
	if _, e = s.router.Call("POST", wifi+"/wps-push-button", Row{".id": target[".id"]}); e != nil {
		return fail(errors.New("RouterOS не стартира WPS за този интерфейс. Възможно е виртуалната мрежа да не го поддържа"))
	}
	snap.RestoreStates = false
	if e = s.store.change(func(d *Data) { d.WPS = clone(snap) }); e != nil {
		return fail(e)
	}
	s.event("ok", "WPS е активен за "+str(target["configuration.ssid"])+" за 2 минути")
	time.AfterFunc(120*time.Second, func() { s.expireWPS(snap.Until) })
	return Row{"until": snap.Until}, nil
}
func (s *Service) expireWPS(until int64) {
	if !s.mutationLock() {
		time.AfterFunc(time.Second, func() { s.expireWPS(until) })
		return
	}
	defer s.gate.Unlock()
	w := s.store.read().WPS
	if w != nil && w.Until == until {
		if e := s.recoverWPS(); e != nil {
			s.event("error", e.Error())
		}
	}
}
func (s *Service) recoverWPS() error {
	w := s.store.read().WPS
	if w == nil {
		return nil
	}
	if w.Interface == s.config.Protected5G {
		return errors.New("Невалиден WPS журнал: защитеното радио")
	}
	if !s.demo {
		rows, radio, _, e := s.topology()
		if e != nil {
			return e
		}
		t := byName(rows, w.Interface)
		if t == nil || !s.managed(t, radio) {
			return errors.New("WPS интерфейсът не е потвърден")
		}
		if e = s.router.Patch(wifi, str(t[".id"]), Row{"security.wps": "disabled"}); e != nil {
			return e
		}
		if w.HadDirect {
			if e = s.router.Patch(wifi, str(t[".id"]), Row{"security.wps": w.Direct}); e != nil {
				return e
			}
		} else {
			if _, e = s.router.Call("POST", "execute", Row{"script": "/interface/wifi/unset value-name=security.wps " + selectROS(wifi, "name", w.Interface), "as-string": ""}); e != nil {
				return e
			}
		}
		if w.RestoreStates {
			if e = s.router.Patch(wifi, str(t[".id"]), Row{"disabled": w.WasDisabled}); e != nil {
				return e
			}
			if w.Radio != s.config.Radio2G {
				return errors.New("Невалиден родител в WPS журнала")
			}
			if e = s.router.Patch(wifi, str(radio[".id"]), Row{"disabled": w.RadioWasDisabled}); e != nil {
				return e
			}
			if e = s.verifyNetworkStates([]Row{{"name": w.Interface, "disabled": w.WasDisabled}, {"name": w.Radio, "disabled": w.RadioWasDisabled}}); e != nil {
				return e
			}
		}
	}
	if e := s.store.change(func(d *Data) { d.WPS = nil }); e != nil {
		return e
	}
	s.event("ok", "WPS е спрян. Мрежата остава включена.")
	return nil
}
func (s *Service) stopWPS() (Row, error) {
	if !s.mutationLock() {
		return nil, errors.New("Изчакай текущата операция")
	}
	defer s.gate.Unlock()
	return Row{"stopped": true}, s.recoverWPS()
}
