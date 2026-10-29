package limits

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "30m", want: 30 * time.Minute},
		{in: "1h30m", want: 90 * time.Minute},
		{in: "14d", want: 14 * 24 * time.Hour},
		{in: "", wantErr: true},
		{in: "0s", wantErr: true},
		{in: "-5m", wantErr: true},
		{in: "0d", wantErr: true},
		{in: "1.5d", wantErr: true},
		{in: "ten", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{in: "6G", want: 6 << 30},
		{in: "512M", want: 512 << 20},
		{in: "64K", want: 64 << 10},
		{in: "1T", want: 1 << 40},
		{in: "1048576", want: 1 << 20},
		{in: "", wantErr: true},
		{in: "0", wantErr: true},
		{in: "0G", wantErr: true},
		{in: "6g", wantErr: true},
		{in: "6GB", wantErr: true},
		{in: "1.5G", wantErr: true},
		{in: "-1G", wantErr: true},
		{in: "infinity", wantErr: true},
		{in: "99999999999999T", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMemory(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseCPUQuota(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "400%", want: 400},
		{in: "50%", want: 50},
		{in: "400", wantErr: true},
		{in: "0%", wantErr: true},
		{in: "-1%", wantErr: true},
		{in: "%", wantErr: true},
		{in: "1.5%", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseCPUQuota(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSpecParse(t *testing.T) {
	got, err := Spec{Timeout: "20m", MemoryMax: "6G", TasksMax: 100, CPUQuota: "200%"}.Parse()
	if err != nil {
		t.Fatal(err)
	}
	want := Limits{Timeout: 20 * time.Minute, MemoryMax: 6 << 30, TasksMax: 100, CPUQuota: 200}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	empty, err := Spec{}.Parse()
	if err != nil || empty != (Limits{}) {
		t.Errorf("empty spec = %+v, %v; want zero, nil", empty, err)
	}

	for name, spec := range map[string]Spec{
		"timeout":    {Timeout: "x"},
		"memory_max": {MemoryMax: "x"},
		"tasks_max":  {TasksMax: -1},
		"cpu_quota":  {CPUQuota: "x"},
	} {
		if _, err := spec.Parse(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestClamp(t *testing.T) {
	caps := Limits{Timeout: 30 * time.Minute, MemoryMax: 8 << 30, TasksMax: 4096, CPUQuota: 400}
	tests := []struct {
		name string
		req  Limits
		want Limits
	}{
		{name: "unset takes caps", req: Limits{}, want: caps},
		{
			name: "lower kept",
			req:  Limits{Timeout: time.Minute, MemoryMax: 1 << 30, TasksMax: 10, CPUQuota: 100},
			want: Limits{Timeout: time.Minute, MemoryMax: 1 << 30, TasksMax: 10, CPUQuota: 100},
		},
		{
			name: "higher clamped",
			req:  Limits{Timeout: time.Hour, MemoryMax: 16 << 30, TasksMax: 9999, CPUQuota: 800},
			want: caps,
		},
		{
			name: "mixed",
			req:  Limits{Timeout: time.Hour, MemoryMax: 6 << 30},
			want: Limits{Timeout: 30 * time.Minute, MemoryMax: 6 << 30, TasksMax: 4096, CPUQuota: 400},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caps.Clamp(tt.req); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWithDefaults(t *testing.T) {
	d := Limits{Timeout: time.Hour, MemoryMax: 1 << 30, TasksMax: 10, CPUQuota: 100}
	got := Limits{Timeout: time.Minute, CPUQuota: 800}.WithDefaults(d)
	want := Limits{Timeout: time.Minute, MemoryMax: 1 << 30, TasksMax: 10, CPUQuota: 800}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
