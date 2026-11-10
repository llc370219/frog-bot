package scheduler

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRestartPreservesDueOccurrence(t *testing.T) {
	for _, kind := range []Kind{Daily, Once} {
		for _, late := range []time.Duration{time.Minute, 3 * time.Hour} {
			t.Run(string(kind)+late.String(), func(t *testing.T) {
				before := time.Date(2026, 11, 10, 8, 59, 0, 0, time.UTC)
				clock, host := &fakeClock{t: before}, &fakeHost{}
				dir := t.TempDir()
				st, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				e := NewEngine(st, host, Config{MisfireGrace: 5 * time.Minute, OneOffGrace: 2 * time.Hour})
				e.Clock = clock
				due := before.Add(time.Minute)
				p, err := e.Upsert(Draft{Title: "test", Prompt: "test", Enabled: true, Spec: Spec{Kind: kind, Timezone: "UTC", Hour: 9, AtLocal: due}})
				if err != nil {
					t.Fatal(err)
				}
				clock.set(due.Add(late))
				reopened, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				restarted := NewEngine(reopened, host, e.Cfg)
				restarted.Clock = clock
				restarted.recoverLocked()
				restarted.Tick()
				restarted.Tick()
				want := 1
				status := "dispatched"
				if late > 2*time.Hour {
					want = 0
					status = "skipped_misfire"
					if kind == Once {
						status = "expired"
					}
				}
				if len(host.dispatched) != want {
					t.Fatalf("dispatched=%d want %d", len(host.dispatched), want)
				}
				c, ok := reopened.Claim(OccurrenceKey(p.ID, due))
				if !ok || c.Status != status {
					t.Fatalf("claim=%+v present=%v want %s", c, ok, status)
				}
			})
		}
	}
}

// Point only the save target at a directory: temp-file writing succeeds but
// atomic rename fails. This works regardless of temporary naming or user IDs.
func failScheduleSaves(t *testing.T, s *Store) func() {
	t.Helper()
	original := s.path
	s.path = filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	return func() { s.path = original }
}

func TestScheduleMutationsRollbackOnSaveFailure(t *testing.T) {
	for _, op := range []string{"put", "delete", "claim", "update_claim", "next"} {
		t.Run(op, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err := s.Put(&Plan{ID: "p", Title: "original", NextRun: &now}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.TryClaim(Claim{Key: "existing", PlanID: "p", Status: "claimed"}); err != nil {
				t.Fatal(err)
			}
			plans, claims := s.List(), s.Claims()
			restore := failScheduleSaves(t, s)
			switch op {
			case "put":
				err = s.Put(&Plan{ID: "p", Title: "failed update"})
			case "delete":
				err = s.Delete("p")
			case "claim":
				_, _, err = s.TryClaim(Claim{Key: "new", PlanID: "p", Status: "claimed"})
			case "update_claim":
				_, err = s.UpdateClaim("existing", "dispatched", "failed metadata")
			case "next":
				err = s.SetNext("p", nil)
			}
			restore()
			if err == nil {
				t.Fatal("expected injected save failure")
			}
			if !reflect.DeepEqual(s.List(), plans) || !reflect.DeepEqual(s.Claims(), claims) {
				t.Fatal("failed save changed memory")
			}
			if err := s.Put(&Plan{ID: "other"}); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := reopened.Get("p")
			if !reflect.DeepEqual(p, plans[0]) || !reflect.DeepEqual(reopened.Claims(), claims) {
				t.Fatal("later save committed an earlier failed mutation")
			}
		})
	}
}

func TestScheduleSaveReplacesFileAtomically(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backup := s.path + ".snapshot"
	if err := os.Link(s.path, backup); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Plan{ID: "new"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("old inode was overwritten rather than replaced")
	}
}

func TestScheduleStoreDoesNotExposeMutablePointers(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	p := Plan{ID: "p", NextRun: &now, Spec: Spec{Weekdays: []time.Weekday{time.Monday}}}
	if err := s.Put(&p); err != nil {
		t.Fatal(err)
	}
	want := now
	p.Spec.Weekdays[0] = time.Friday
	now = now.Add(time.Hour)
	got, _ := s.Get("p")
	got.Spec.Weekdays[0] = time.Sunday
	*got.NextRun = now
	stored, _ := s.Get("p")
	if stored.Spec.Weekdays[0] != time.Monday || !stored.NextRun.Equal(want) {
		t.Fatal("caller mutated stored state without saving")
	}
}
