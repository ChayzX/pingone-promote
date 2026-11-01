package app

import (
	"pingone-promote/internal/operation"
	"pingone-promote/internal/store"
)

func safeObservedStatus(status string) string {
	if operation.Classify(status) == operation.Unknown {
		return "UNKNOWN"
	}
	return status
}

func recordDispatch(record *store.Record) operation.DispatchState {
	if record.Execution == nil {
		if record.Intent != "" {
			return operation.Uncertain
		}
		return operation.NotDispatched
	}
	evidence := record.Execution
	if evidence.Version != 1 || evidence.EffectsVerified {
		return ""
	}
	if evidence.DispatchState == operation.Uncertain {
		return operation.Uncertain
	}
	if evidence.DispatchState == operation.Identified && evidence.NativeObserved {
		return operation.Identified
	}
	// Unsupported metadata is readable but cannot authorize another dispatch.
	return ""
}

func projectExecution(view *PlanView, record *store.Record, observed bool) {
	view.DispatchState = recordDispatch(record)
	view.NativeObserved = observed
	decision := operation.Evaluate(operation.Observation{Status: view.Status, Dispatch: view.DispatchState})
	view.EffectsVerified = false
	view.NeedsReconciliation = decision.NeedsReconciliation
	view.MayHaveEffects = decision.MayHaveEffects
}
