package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"frogbot/internal/atomicfile"
)

const storeVersion = 1

type fileFormat struct {
	Version int               `json:"version"`
	Plans   map[string]*Plan  `json:"plans"`
	Claims  map[string]*Claim `json:"claims"`
}

// Store 是线程安全的计划/认领持久化（data/schedules.json）。
type Store struct {
	path string
	mu   sync.Mutex
	data fileFormat
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "schedules.json"), data: fileFormat{
		Version: storeVersion, Plans: map[string]*Plan{}, Claims: map[string]*Claim{},
	}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, s.saveLocked()
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("schedules.json 为空，拒绝静默丢失计划")
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("读取 schedules.json：%w", err)
	}
	if s.data.Plans == nil {
		s.data.Plans = map[string]*Plan{}
	}
	if s.data.Claims == nil {
		s.data.Claims = map[string]*Claim{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	s.data.Version = storeVersion
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, raw, 0o600)
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func clonePlan(p Plan) Plan {
	p.NextRun, p.LastRun = cloneTime(p.NextRun), cloneTime(p.LastRun)
	p.Spec.Weekdays = append([]time.Weekday(nil), p.Spec.Weekdays...)
	return p
}

func cloneClaim(c Claim) Claim {
	c.DispatchedAt, c.FinishedAt = cloneTime(c.DispatchedAt), cloneTime(c.FinishedAt)
	return c
}

// Mutate a detached snapshot so an unsuccessful save leaves no phantom state.
func (s *Store) updateLocked(change func()) error {
	prev := s.data
	s.data = fileFormat{Version: storeVersion, Plans: map[string]*Plan{}, Claims: map[string]*Claim{}}
	for id, p := range prev.Plans {
		cp := clonePlan(*p)
		s.data.Plans[id] = &cp
	}
	for id, c := range prev.Claims {
		cp := cloneClaim(*c)
		s.data.Claims[id] = &cp
	}
	change()
	if err := s.saveLocked(); err != nil {
		s.data = prev
		return err
	}
	return nil
}

func (s *Store) Put(p *Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(func() { cp := clonePlan(*p); s.data.Plans[p.ID] = &cp })
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Plans[id]; !ok {
		return errors.New("没有这个计划")
	}
	return s.updateLocked(func() { delete(s.data.Plans, id) })
}

func (s *Store) Get(id string) (Plan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data.Plans[id]
	if !ok {
		return Plan{}, false
	}
	return clonePlan(*p), true
}

func (s *Store) List() []Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Plan, 0, len(s.data.Plans))
	for _, p := range s.data.Plans {
		out = append(out, clonePlan(*p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		ni, nj := out[i].NextRun, out[j].NextRun
		switch {
		case ni == nil && nj == nil:
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		case ni == nil:
			return false
		case nj == nil:
			return true
		default:
			return ni.Before(*nj)
		}
	})
	return out
}

// TryClaim 原子认领一次触发：已有同 key 则返回已有记录和 created=false。
func (s *Store) TryClaim(c Claim) (Claim, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.data.Claims[c.Key]; ok {
		return cloneClaim(*old), false, nil
	}
	err := s.updateLocked(func() {
		cp := cloneClaim(c)
		s.data.Claims[c.Key] = &cp
		if p := s.data.Plans[c.PlanID]; p != nil {
			t := c.IntendedAt
			p.LastRun, p.LastStatus, p.LastTaskID, p.LastClaim = &t, c.Status, c.TaskID, c.Key
			p.UpdatedAt = time.Now().UTC()
		}
	})
	if err != nil {
		return Claim{}, false, err
	}
	return cloneClaim(c), true, nil
}

func (s *Store) UpdateClaim(key, status, note string) (Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Claims[key]
	if !ok {
		return Claim{}, errors.New("没有这条认领")
	}
	err := s.updateLocked(func() {
		c := s.data.Claims[key]
		c.Status = status
		if note != "" {
			c.Note = note
		}
		now := time.Now().UTC()
		switch status {
		case "dispatched":
			c.DispatchedAt = &now
		case "completed", "failed", "needs_verify", "skipped_misfire", "skipped_overlap", "expired":
			c.FinishedAt = &now
		}
		if p := s.data.Plans[c.PlanID]; p != nil {
			p.LastStatus = status
			p.UpdatedAt = now
		}
	})
	if err != nil {
		return Claim{}, err
	}
	return cloneClaim(*s.data.Claims[key]), nil
}

func (s *Store) Claim(key string) (Claim, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Claims[key]
	if !ok {
		return Claim{}, false
	}
	return cloneClaim(*c), true
}

func (s *Store) Claims() []Claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Claim, 0, len(s.data.Claims))
	for _, c := range s.data.Claims {
		out = append(out, cloneClaim(*c))
	}
	return out
}

// SetNext 更新计划的下次触发时间。
func (s *Store) SetNext(id string, next *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Plans[id]
	if !ok {
		return errors.New("没有这个计划")
	}
	return s.updateLocked(func() {
		p := s.data.Plans[id]
		p.NextRun = cloneTime(next)
		p.UpdatedAt = time.Now().UTC()
	})
}

// OpenClaimsFor 返回某计划仍未结束的认领。
func (s *Store) OpenClaimsFor(planID string) []Claim {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Claim
	for _, c := range s.data.Claims {
		if c.PlanID != planID {
			continue
		}
		switch c.Status {
		case "claimed", "dispatched":
			out = append(out, cloneClaim(*c))
		}
	}
	return out
}
