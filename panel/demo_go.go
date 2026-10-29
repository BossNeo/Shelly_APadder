package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type DemoRouter struct {
	mu     sync.Mutex
	tables map[string][]Row
}

func demoRouter() (Config, RouterAPI) {
	c := Config{AllowChanges: true, ExpectedBoard: "Chateau LTE7 ax · СИМУЛАЦИЯ", Radio2G: "wifi2", Protected5G: "wifi1", Bridge: "bridge", Existing: []string{"sp-demo1", "sp-demo2", "sp-demo3"}, Hosts: []string{"127.0.0.1:8088", "localhost:8088"}}
	r := &DemoRouter{tables: map[string][]Row{"system/resource": {{"board-name": c.ExpectedBoard, "version": "7.x", "free-memory": "805306368", "free-hdd-space": "73400320", "cpu-load": "2", "uptime": "2h"}}, wifi: {{".id": "*1", "name": "wifi1", "channel.band": "5ghz-ax", "configuration.mode": "ap", "configuration.ssid": "MikroTik · Управление", "disabled": "false"}, {".id": "*2", "name": "wifi2", "channel.band": "2ghz-ax", "configuration.mode": "ap", "configuration.ssid": "Работна мрежа", "disabled": "false"}}, "interface/bridge": {{".id": "*B", "name": "bridge"}}}}
	for i := 1; i <= 3; i++ {
		r.tables[wifi] = append(r.tables[wifi], Row{".id": fmt.Sprint("*P", i), "name": fmt.Sprint("sp-demo", i), "master-interface": "wifi2", "configuration.ssid": fmt.Sprint("Project-", i), "disabled": "true"})
	}
	return c, r
}
func seedDemo(s *Store) {
	if len(s.read().Projects) > 0 {
		return
	}
	_ = s.change(func(d *Data) {
		for i, title := range []string{"Обект · София", "Работилница", "Тестова мрежа"} {
			n := i + 1
			d.Projects = append(d.Projects, Project{ID: fmt.Sprint(n), Title: title, SSID: fmt.Sprint("Project-", n), Password: "demo-password", Interface: fmt.Sprint("sp-demo", n), NextNumber: 1})
		}
	})
}
func (d *DemoRouter) Rows(path string, _ Row) ([]Row, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := clone(d.tables[path])
	if out == nil {
		out = []Row{}
	}
	return out, nil
}
func (d *DemoRouter) Call(string, string, Row) (any, error) {
	return nil, errors.New("Симулацията не изпълнява RouterOS скриптове")
}
func (d *DemoRouter) Patch(path, id string, body Row) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, v := range d.tables[path] {
		if str(v[".id"]) == id {
			for k, b := range body {
				v[k] = b
			}
			return nil
		}
	}
	return errors.New("Липсва демонстрационен запис")
}
func (d *DemoRouter) Add(path string, body Row) (Row, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := copyRow(body)
	row[".id"] = "*" + token()[:8]
	d.tables[path] = append(d.tables[path], row)
	return copyRow(row), nil
}
func (d *DemoRouter) Remove(path, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []Row{}
	for _, v := range d.tables[path] {
		if str(v[".id"]) != id {
			out = append(out, v)
		}
	}
	d.tables[path] = out
	return nil
}
func (d *DemoRouter) RPC(string, string, Row) (Row, error) {
	return nil, errors.New("Симулацията не се свързва с Shelly")
}
func (s *Service) runDemo(kind string, devices []Row) {
	defer s.gate.Unlock()
	s.sleep(time.Second)
	s.status.Lock()
	defer s.status.Unlock()
	if kind == "scan" {
		s.job.Found = []Row{}
		for i := 1; i <= 3; i++ {
			s.job.Found = append(s.job.Found, Row{"ssid": fmt.Sprintf("ShellyPlus1-441793ABCD0%d", i), "signal": fmt.Sprint(-40 - i*5)})
		}
	} else {
		for i, v := range devices {
			s.job.Results = append(s.job.Results, Row{"ssid": v["ssid"], "state": "verified", "ip": fmt.Sprint("192.168.89.", 100+i)})
		}
	}
	s.job.State = "done"
	if s.cancel.Load() {
		s.job.State = "cancelled"
	}
	s.job.Message = "Симулацията приключи. Няма промени по рутер."
}
