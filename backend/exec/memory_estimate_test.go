package exec

import (
	"testing"

	"kanban/backend/types"
)

func TestEstimateTaskMemory(t *testing.T) {
	tests := []struct {
		name      string
		peakMB    int64
		avgPeakMB int64
		want      int64
	}{
		{"uses own peak when recorded", 512, 256, 512},
		{"falls back to average when no peak", 0, 256, 256},
		{"zero when no peak and no average", 0, 0, 0},
		{"peak of one is used not average", 1, 1024, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := types.Task{SandboxMemoryPeakMB: tc.peakMB}
			if got := estimateTaskMemory(task, tc.avgPeakMB); got != tc.want {
				t.Errorf("estimateTaskMemory(%d, %d) = %d, want %d", tc.peakMB, tc.avgPeakMB, got, tc.want)
			}
		})
	}
}

func TestComputeAvgPeakMB(t *testing.T) {
	tests := []struct {
		name  string
		tasks []types.Task
		want  int64
	}{
		{"empty slice returns 0", nil, 0},
		{"all zero peaks returns 0", []types.Task{{SandboxMemoryPeakMB: 0}, {SandboxMemoryPeakMB: 0}}, 0},
		{"single peak", []types.Task{{SandboxMemoryPeakMB: 200}}, 200},
		{"mean of two", []types.Task{{SandboxMemoryPeakMB: 200}, {SandboxMemoryPeakMB: 400}}, 300},
		{"zeros excluded from mean", []types.Task{{SandboxMemoryPeakMB: 0}, {SandboxMemoryPeakMB: 200}, {SandboxMemoryPeakMB: 400}}, 300},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeAvgPeakMB(tc.tasks); got != tc.want {
				t.Errorf("computeAvgPeakMB() = %d, want %d", got, tc.want)
			}
		})
	}
}
