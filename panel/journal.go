package main

import "time"

type Event struct {
	ID      uint64 `json:"id"`
	Time    int64  `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (s *Service) event(level, message string) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	s.eventID++
	s.events = append(s.events, Event{s.eventID, time.Now().Unix(), level, message})
	if len(s.events) > 200 {
		s.events = append([]Event(nil), s.events[len(s.events)-200:]...)
	}
}
func (s *Service) journal() []Event {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	out := append([]Event{}, s.events...)
	return out
}
