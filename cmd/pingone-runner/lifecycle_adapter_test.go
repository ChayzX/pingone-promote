package main

import (
	"context"
	"testing"
	"time"

	"pingone-promote/internal/pingone"
)

func TestLifecycleAppliedChildTerminalPolling(t *testing.T) {
	for _, status := range []string{"COMPLETED", "ERROR", "ROLLED_BACK", "READY", "IN_PROGRESS", "ROLLBACK_IN_PROGRESS", "VALIDATION_FAILED", "completed", "FUTURE", ""} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			got, err := waitForTerminal(context.Background(), func(context.Context) (pingone.Promotion, error) {
				calls++
				return pingone.Promotion{ID: "child", Status: "COMPLETED"}, nil
			}, pingone.Promotion{ID: "child", Status: status}, time.Second, time.Millisecond)
			if err != nil || got.ID != "child" || got.Status != "COMPLETED" && got.Status != "ERROR" {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			want := 1
			if status == "COMPLETED" || status == "ERROR" {
				want = 0
			}
			if calls != want {
				t.Fatalf("poll calls=%d want=%d", calls, want)
			}
		})
	}
}

func TestLifecyclePollingDoesNotTerminateOnRestoredParentState(t *testing.T) {
	calls := 0
	got, err := waitForTerminal(context.Background(), func(context.Context) (pingone.Promotion, error) {
		calls++
		status := "ROLLED_BACK"
		if calls == 2 {
			status = "ERROR"
		}
		return pingone.Promotion{ID: "child", Status: status}, nil
	}, pingone.Promotion{ID: "child", Status: "IN_PROGRESS"}, time.Second, time.Millisecond)
	if err != nil || calls != 2 || got.Status != "ERROR" || got.ID != "child" {
		t.Fatalf("got=%+v calls=%d err=%v", got, calls, err)
	}
}

func TestLifecycleUnknownPollingRetainsUncertainOutcome(t *testing.T) {
	for _, status := range []string{"FUTURE", "completed", "VALIDATION_FAILED", "ROLLED_BACK"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := waitForTerminal(ctx, func(context.Context) (pingone.Promotion, error) {
				cancel()
				return pingone.Promotion{ID: "child", Status: status}, nil
			}, pingone.Promotion{ID: "child", Status: "IN_PROGRESS"}, time.Second, time.Millisecond)
			if err == nil || got.Status != status {
				t.Fatalf("unknown poll completed: got=%+v err=%v", got, err)
			}
		})
	}
}
