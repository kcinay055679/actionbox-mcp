package limits

import (
	"errors"
	"fmt"
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// zero field = unset
type Limits struct {
	Timeout   time.Duration
	MemoryMax uint64 // bytes
	TasksMax  int
	CPUQuota  int // percent
}

type Spec struct {
	Timeout   string `toml:"timeout"`
	MemoryMax string `toml:"memory_max"`
	TasksMax  int    `toml:"tasks_max"`
	CPUQuota  string `toml:"cpu_quota"`
}

func (s Spec) Parse() (Limits, error) {
	var l Limits
	var err error
	if s.Timeout != "" {
		if l.Timeout, err = ParseDuration(s.Timeout); err != nil {
			return Limits{}, fmt.Errorf("timeout: %w", err)
		}
	}
	if s.MemoryMax != "" {
		if l.MemoryMax, err = ParseMemory(s.MemoryMax); err != nil {
			return Limits{}, fmt.Errorf("memory_max: %w", err)
		}
	}
	if s.TasksMax < 0 {
		return Limits{}, fmt.Errorf("tasks_max: must not be negative, got %d", s.TasksMax)
	}
	l.TasksMax = s.TasksMax
	if s.CPUQuota != "" {
		if l.CPUQuota, err = ParseCPUQuota(s.CPUQuota); err != nil {
			return Limits{}, fmt.Errorf("cpu_quota: %w", err)
		}
	}
	return l, nil
}

func (caps Limits) Clamp(req Limits) Limits {
	return Limits{
		Timeout:   clamp(req.Timeout, caps.Timeout),
		MemoryMax: clamp(req.MemoryMax, caps.MemoryMax),
		TasksMax:  clamp(req.TasksMax, caps.TasksMax),
		CPUQuota:  clamp(req.CPUQuota, caps.CPUQuota),
	}
}

func (l Limits) WithDefaults(d Limits) Limits {
	return Limits{
		Timeout:   orDefault(l.Timeout, d.Timeout),
		MemoryMax: orDefault(l.MemoryMax, d.MemoryMax),
		TasksMax:  orDefault(l.TasksMax, d.TasksMax),
		CPUQuota:  orDefault(l.CPUQuota, d.CPUQuota),
	}
}

func orDefault[T time.Duration | uint64 | int](v, d T) T {
	if v == 0 {
		return d
	}
	return v
}

func clamp[T time.Duration | uint64 | int](req, limit T) T {
	if req == 0 || req > limit {
		return limit
	}
	return req
}

var daysPattern = regexp.MustCompile(`^([0-9]+)d$`)

// time.ParseDuration lacks days, retention uses "14d"
func ParseDuration(s string) (time.Duration, error) {
	d, err := parseDays(s)
	if err != nil {
		d, err = time.ParseDuration(s)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return d, nil
}

func parseDays(s string) (time.Duration, error) {
	m := daysPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("not days")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > int64(time.Duration(1<<63-1)/(24*time.Hour)) {
		return 0, errors.New("days out of range")
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

var memoryPattern = regexp.MustCompile(`^([0-9]+)([KMGT]?)$`)

var memoryShift = map[string]int{"": 0, "K": 10, "M": 20, "G": 30, "T": 40}

// 1024-based suffixes, as systemd MemoryMax=
func ParseMemory(s string) (uint64, error) {
	m := memoryPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid memory size %q, want e.g. 6G", s)
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	shift := memoryShift[m[2]]
	if err != nil || bits.LeadingZeros64(n) < shift {
		return 0, fmt.Errorf("memory size %q out of range", s)
	}
	if n == 0 {
		return 0, fmt.Errorf("memory size %q must be positive", s)
	}
	return n << shift, nil
}

func ParseCPUQuota(s string) (int, error) {
	digits, ok := strings.CutSuffix(s, "%")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || strings.HasPrefix(digits, "+") || n <= 0 {
		return 0, fmt.Errorf("invalid cpu quota %q, want e.g. 400%%", s)
	}
	return n, nil
}
