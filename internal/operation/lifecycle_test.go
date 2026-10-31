package operation

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		status string
		want   NativeState
	}{
		{"NEW", Preparing},
		{"PREPARING", Preparing},
		{"ADDITIONAL_SNAPSHOTS_REQUESTED", Preparing},
		{"ROLLBACK_NEW", Preparing},
		{"ROLLBACK_PREPARING", Preparing},
		{"READY", Ready},
		{"IN_PROGRESS", Running},
		{"ROLLBACK_IN_PROGRESS", Running},
		{"COMPLETED", Completed},
		{"ERROR", Failed},
		{"ROLLED_BACK", RolledBack},
		{"VALIDATION_FAILED", Invalid},
		{"", Unknown},
		{"ready", Unknown},
		{"FUTURE_STATUS", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := Classify(tt.status); got != tt.want {
				t.Fatalf("Classify(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name        string
		observation Observation
		want        Decision
	}{
		{"ready and not dispatched can execute", Observation{Status: "READY", Dispatch: NotDispatched}, Decision{Native: Ready, CanExecute: true}},
		{"uncertain dispatch needs reconciliation and may have effects", Observation{Status: "READY", Dispatch: Uncertain}, Decision{Native: Ready, NeedsReconciliation: true, MayHaveEffects: true}},
		{"identified ready needs reconciliation and may have effects", Observation{Status: "READY", Dispatch: Identified}, Decision{Native: Ready, NeedsReconciliation: true, MayHaveEffects: true}},
		{"unknown needs reconciliation", Observation{Status: "future", Dispatch: NotDispatched}, Decision{Native: Unknown, NeedsReconciliation: true}},
		{"running may have effects", Observation{Status: "IN_PROGRESS", Dispatch: NotDispatched}, Decision{Native: Running, MayHaveEffects: true}},
		{"completed may have effects but verification stays independent", Observation{Status: "COMPLETED", Dispatch: NotDispatched}, Decision{Native: Completed, MayHaveEffects: true}},
		{"failed may have effects", Observation{Status: "ERROR", Dispatch: NotDispatched}, Decision{Native: Failed, MayHaveEffects: true}},
		{"rolled back may have effects", Observation{Status: "ROLLED_BACK", Dispatch: NotDispatched}, Decision{Native: RolledBack, MayHaveEffects: true}},
		{"effects verified is preserved independently", Observation{Status: "COMPLETED", Dispatch: NotDispatched, EffectsVerified: true}, Decision{Native: Completed, MayHaveEffects: true, EffectsVerified: true}},
		{"preparing does not execute", Observation{Status: "PREPARING", Dispatch: NotDispatched}, Decision{Native: Preparing}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Evaluate(tt.observation); got != tt.want {
				t.Fatalf("Evaluate(%+v) = %+v, want %+v", tt.observation, got, tt.want)
			}
		})
	}
}
