package server

import (
	"testing"
	"time"

	"github.com/modelsphere/swiss/internal/cluster"
)

func TestPodStuckTerminating(t *testing.T) {
	del := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	ready := func(name string) (bool, bool) {
		switch name {
		case "gpu-1":
			return true, true
		case "gpu-down":
			return false, true
		default:
			return false, false
		}
	}
	window := 30*time.Second + 5*time.Minute
	cases := []struct {
		name  string
		pod   cluster.Pod
		now   time.Time
		nodes func(string) (bool, bool)
		want  bool
	}{
		{
			name:  "not terminating",
			pod:   cluster.Pod{Phase: "Running", Node: "gpu-down"},
			now:   del,
			nodes: ready,
		},
		{
			name:  "within deletion grace on a ready node",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30), TerminationGracePeriodSeconds: secs(90), Node: "gpu-1"},
			now:   del.Add(window),
			nodes: ready,
		},
		{
			name:  "past deletion grace",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30), TerminationGracePeriodSeconds: secs(90), Node: "gpu-1"},
			now:   del.Add(window + time.Nanosecond),
			nodes: ready,
			want:  true,
		},
		{
			name:  "termination grace when deletion grace is unset",
			pod:   cluster.Pod{DeletionTimestamp: &del, TerminationGracePeriodSeconds: secs(10), Node: "gpu-1"},
			now:   del.Add(10*time.Second + 5*time.Minute + time.Second),
			nodes: ready,
			want:  true,
		},
		{
			name:  "default 30s is not yet stuck",
			pod:   cluster.Pod{DeletionTimestamp: &del, Node: "gpu-1"},
			now:   del.Add(30*time.Second + 5*time.Minute),
			nodes: ready,
		},
		{
			name:  "default 30s just past",
			pod:   cluster.Pod{DeletionTimestamp: &del, Node: "gpu-1"},
			now:   del.Add(30*time.Second + 5*time.Minute + time.Nanosecond),
			nodes: ready,
			want:  true,
		},
		{
			name:  "zero deletion grace is set and beats termination grace",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(0), TerminationGracePeriodSeconds: secs(90), Node: "gpu-1"},
			now:   del.Add(5*time.Minute + time.Nanosecond),
			nodes: ready,
			want:  true,
		},
		{
			name:  "notready node inside the window",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(600), Node: "gpu-down"},
			now:   del,
			nodes: ready,
			want:  true,
		},
		{
			name:  "node missing from the list",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(600), Node: "gpu-gone"},
			now:   del,
			nodes: ready,
			want:  true,
		},
		{
			name:  "unscheduled pod inside the window",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30)},
			now:   del,
			nodes: ready,
		},
		{
			name:  "unscheduled pod past the window",
			pod:   cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30)},
			now:   del.Add(window + time.Second),
			nodes: ready,
			want:  true,
		},
		{
			name: "nodes unread keeps a pod inside the window",
			pod:  cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30), Node: "gpu-down"},
			now:  del,
		},
		{
			name: "nodes unread still honors the time rule",
			pod:  cluster.Pod{DeletionTimestamp: &del, DeletionGracePeriodSeconds: secs(30), Node: "gpu-down"},
			now:  del.Add(window + time.Second),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := podStuckTerminating(tc.pod, tc.now, tc.nodes); got != tc.want {
				t.Fatalf("stuck=%v, want %v", got, tc.want)
			}
		})
	}
}

func secs(n int64) *int64 { return &n }
