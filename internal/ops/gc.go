// Package ops implements Frog Bot V2 operational resilience:
//   - Automated daily Disk Garbage Collector (48h tmp_* pointer cleanup, 7d/500MB CLI log & screenshot cap)
//   - One-click encrypted asset backup & restore (`frogctl backup/restore`) using scrypt + AES-256-GCM
package ops

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GCConfig controls daily disk reclamation limits for small 2C2G VPS disks.
type GCConfig struct {
	PointerMaxAge time.Duration // default 48h for workspace/tmp_*.txt
	LogMaxAge     time.Duration // default 7 days for logs/cli_*.log and screenshots
	MaxLogBytes   int64         // default 500 MiB total across logs/screenshots
	Now           func() time.Time
}

// GCReport summarizes reclaimed files and bytes from a GC sweep.
type GCReport struct {
	RanAt               time.Time `json:"ran_at"`
	PointersRemoved     int       `json:"pointers_removed"`
	LogsRemovedByAge    int       `json:"logs_removed_by_age"`
	LogsRemovedByCap    int       `json:"logs_removed_by_cap"`
	BytesReclaimed      int64     `json:"bytes_reclaimed"`
	RemainingLogBytes   int64     `json:"remaining_log_bytes"`
}

type DiskGC struct {
	RootDir    string
	ExtraRoots []string // e.g. legacy workspace-bots dir
	Config     GCConfig
}

func NewDiskGC(rootDir string, cfg GCConfig, extraRoots ...string) *DiskGC {
	if cfg.PointerMaxAge <= 0 {
		cfg.PointerMaxAge = 48 * time.Hour
	}
	if cfg.LogMaxAge <= 0 {
		cfg.LogMaxAge = 7 * 24 * time.Hour
	}
	if cfg.MaxLogBytes <= 0 {
		cfg.MaxLogBytes = 500 << 20
	}
	return &DiskGC{RootDir: rootDir, ExtraRoots: extraRoots, Config: cfg}
}

func (g *DiskGC) now() time.Time {
	if g.Config.Now != nil {
		return g.Config.Now()
	}
	return time.Now()
}

type gcCandidate struct {
	path    string
	size    int64
	modTime time.Time
}

// RunNow executes one full disk GC sweep immediately.
func (g *DiskGC) RunNow() GCReport {
	now := g.now()
	rep := GCReport{RanAt: now}
	ptrCutoff := now.Add(-g.Config.PointerMaxAge)
	logCutoff := now.Add(-g.Config.LogMaxAge)

	var keptLogs []gcCandidate
	roots := append([]string{g.RootDir}, g.ExtraRoots...)
	seenRoot := map[string]bool{}

	for _, root := range roots {
		if root == "" || seenRoot[root] {
			continue
		}
		seenRoot[root] = true
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			name := d.Name()
			parent := filepath.Base(filepath.Dir(path))

			// 1. Clean expired pointer files (tmp_*.txt older than 48h)
			if strings.HasPrefix(name, "tmp_") && strings.HasSuffix(name, ".txt") {
				if info.ModTime().Before(ptrCutoff) {
					if os.Remove(path) == nil {
						rep.PointersRemoved++
						rep.BytesReclaimed += info.Size()
					}
				}
				return nil
			}

			// 2. Clean CLI logs and historical verification screenshots
			isCLILog := strings.HasPrefix(name, "cli_") && strings.HasSuffix(name, ".log")
			isScreenshot := (parent == "logs" || parent == "screenshots" || parent == "verify_shots") &&
				(strings.HasSuffix(strings.ToLower(name), ".png") || strings.HasSuffix(strings.ToLower(name), ".jpg") || strings.HasSuffix(strings.ToLower(name), ".jpeg"))
			if isCLILog || isScreenshot {
				if info.ModTime().Before(logCutoff) {
					if os.Remove(path) == nil {
						rep.LogsRemovedByAge++
						rep.BytesReclaimed += info.Size()
					}
					return nil
				}
				keptLogs = append(keptLogs, gcCandidate{path: path, size: info.Size(), modTime: info.ModTime()})
			}
			return nil
		})
	}

	// 3. Enforce total log/screenshot byte cap via oldest-first rolling eviction
	var total int64
	for _, c := range keptLogs {
		total += c.size
	}
	if total > g.Config.MaxLogBytes {
		sort.Slice(keptLogs, func(i, j int) bool { return keptLogs[i].modTime.Before(keptLogs[j].modTime) })
		for _, c := range keptLogs {
			if total <= g.Config.MaxLogBytes {
				break
			}
			if os.Remove(c.path) == nil {
				total -= c.size
				rep.LogsRemovedByCap++
				rep.BytesReclaimed += c.size
			}
		}
	}
	rep.RemainingLogBytes = total
	return rep
}

// StartDailyLoop schedules an automatic daily sweep (default 03:30 local time).
func (g *DiskGC) StartDailyLoop(ctx context.Context, onReport func(GCReport)) {
	go func() {
		for {
			now := g.now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 3, 30, 0, 0, now.Location())
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			timer := time.NewTimer(next.Sub(now))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				rep := g.RunNow()
				if onReport != nil {
					onReport(rep)
				}
			}
		}
	}()
}
