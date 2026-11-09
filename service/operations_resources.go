package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/mem"
)

type operationsResource struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Valid  bool    `json:"valid"`
	Detail string  `json:"detail"`
}
type operationsSample struct {
	At        time.Time            `json:"at"`
	Resources []operationsResource `json:"resources"`
}
type operationsSampler struct{ previous *cpu.TimesStat }

func (s *operationsSampler) sample(ctx context.Context) operationsSample {
	sample := operationsSample{At: time.Now()}
	resource := operationsResource{Name: "CPU"}
	times, err := cpu.TimesWithContext(ctx, false)
	if err == nil && len(times) > 0 {
		current := times[0]
		if s.previous != nil {
			// Guest times are already included in User/Nice on Linux.
			total := current.User + current.Nice + current.System + current.Idle + current.Iowait + current.Irq + current.Softirq + current.Steal
			old := s.previous.User + s.previous.Nice + s.previous.System + s.previous.Idle + s.previous.Iowait + s.previous.Irq + s.previous.Softirq + s.previous.Steal
			elapsed := total - old
			idle := current.Idle + current.Iowait - s.previous.Idle - s.previous.Iowait
			if elapsed > 0 && idle >= 0 && idle <= elapsed {
				resource.Value = 100 * (elapsed - idle) / elapsed
				resource.Valid = true
			}
		}
		s.previous = &current
	} else {
		s.previous = nil
	}
	sample.Resources = append(sample.Resources, resource)
	resource = operationsResource{Name: "Memory"}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err == nil && memory.Total > 0 {
		resource.Value = memory.UsedPercent
		resource.Valid = true
		resource.Detail = fmt.Sprintf("available=%d total=%d bytes", memory.Available, memory.Total)
	}
	sample.Resources = append(sample.Resources, resource)
	paths := []string{common.GetDiskCachePath()}
	if *common.LogDir != "" {
		paths = append(paths, *common.LogDir)
	}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		path, _, _ := strings.Cut(common.SQLitePath, "?")
		paths = append(paths, filepath.Dir(path))
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" {
			path = os.TempDir()
		}
		absolute, err := filepath.Abs(path)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		resource = operationsResource{Name: "Disk", Detail: absolute}
		usage, err := disk.UsageWithContext(ctx, absolute)
		if err == nil && usage.Total > 0 {
			resource.Valid = true
			resource.Value = usage.UsedPercent
			resource.Detail = fmt.Sprintf("%s free=%d total=%d bytes", absolute, usage.Free, usage.Total)
		}
		sample.Resources = append(sample.Resources, resource)
	}
	return sample
}

type operationsAlertState struct {
	Since         time.Time
	LastSample    time.Time
	RecoverySince time.Time
	LastSent      time.Time
	Active        bool
	Announced     bool
}
type operationsAlert struct {
	Key      string
	Subject  string
	Content  string
	Recovery bool
}

// Evaluate consumes valid, fresh, consecutive samples. Delivery acknowledgement
// is separate, so queue pressure or SMTP failure does not mark an alert sent.
func (s *operationsAlertState) evaluate(now time.Time, key string, value float64, valid bool, trigger, recovery float64, hold time.Duration, detail string) *operationsAlert {
	if !valid || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		s.Since, s.RecoverySince, s.LastSample = time.Time{}, time.Time{}, time.Time{}
		return nil
	}
	if !s.LastSample.IsZero() && (now.Sub(s.LastSample) > 45*time.Second || now.Before(s.LastSample)) {
		s.Since, s.RecoverySince = time.Time{}, time.Time{}
	}
	s.LastSample = now
	if value >= trigger {
		s.RecoverySince = time.Time{}
		if s.Since.IsZero() {
			s.Since = now
		}
		if now.Sub(s.Since) >= hold {
			s.Active = true
		}
		if s.Active && (s.LastSent.IsZero() || now.Sub(s.LastSent) >= time.Hour) {
			return &operationsAlert{Key: key, Subject: "Resource alert / 资源告警", Content: fmt.Sprintf("%s %.1f%% >= %.1f%%; since %s; %s", key, value, trigger, s.Since.Format(time.RFC3339), detail)}
		}
		return nil
	}
	s.Since = time.Time{}
	if !s.Active {
		return nil
	}
	if value >= recovery {
		s.RecoverySince = time.Time{}
		return nil
	}
	if s.RecoverySince.IsZero() {
		s.RecoverySince = now
	}
	if now.Sub(s.RecoverySince) < 5*time.Minute {
		return nil
	}
	if s.Announced {
		return &operationsAlert{Key: key, Subject: "Resource recovered / 资源恢复", Content: fmt.Sprintf("%s %.1f%% < %.1f%% for 5m; %s", key, value, recovery, detail), Recovery: true}
	}
	*s = operationsAlertState{LastSample: now}
	return nil
}
