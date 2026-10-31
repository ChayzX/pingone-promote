// Package operation defines pure operation lifecycle decisions.
package operation

// NativeState is the normalized state reported by the native service.
type NativeState string

const (
	Unknown    NativeState = "unknown"
	Preparing  NativeState = "preparing"
	Ready      NativeState = "ready"
	Running    NativeState = "running"
	Completed  NativeState = "completed"
	Failed     NativeState = "failed"
	RolledBack NativeState = "rolled_back"
	Invalid    NativeState = "invalid"
)

// DispatchState records how far a request may have been dispatched.
type DispatchState string

const (
	NotDispatched DispatchState = "not_dispatched"
	Uncertain     DispatchState = "uncertain"
	Identified    DispatchState = "identified"
)

// Observation combines native status with local dispatch evidence.
type Observation struct {
	Status          string
	Dispatch        DispatchState
	EffectsVerified bool
}

// Decision describes safe lifecycle actions derived from an observation.
type Decision struct {
	Native              NativeState
	CanExecute          bool
	NeedsReconciliation bool
	MayHaveEffects      bool
	EffectsVerified     bool
}

// Classify maps an exact native status to its normalized lifecycle state.
func Classify(status string) NativeState {
	switch status {
	case "NEW", "PREPARING", "ADDITIONAL_SNAPSHOTS_REQUESTED", "ROLLBACK_NEW", "ROLLBACK_PREPARING":
		return Preparing
	case "READY":
		return Ready
	case "IN_PROGRESS", "ROLLBACK_IN_PROGRESS":
		return Running
	case "COMPLETED":
		return Completed
	case "ERROR":
		return Failed
	case "ROLLED_BACK":
		return RolledBack
	case "VALIDATION_FAILED":
		return Invalid
	default:
		return Unknown
	}
}

// Evaluate derives execution and reconciliation decisions without side effects.
func Evaluate(observation Observation) Decision {
	native := Classify(observation.Status)
	dispatchKnown := observation.Dispatch == NotDispatched || observation.Dispatch == Uncertain || observation.Dispatch == Identified
	dispatchMayHaveEffects := observation.Dispatch == Uncertain || observation.Dispatch == Identified || !dispatchKnown
	mayHaveEffects := native == Running || native == Completed || native == Failed || native == RolledBack || dispatchMayHaveEffects
	needsReconciliation := native == Unknown || !dispatchKnown || observation.Dispatch == Uncertain || (native == Ready && observation.Dispatch == Identified)
	return Decision{
		Native:              native,
		CanExecute:          native == Ready && observation.Dispatch == NotDispatched,
		NeedsReconciliation: needsReconciliation,
		MayHaveEffects:      mayHaveEffects,
		EffectsVerified:     observation.EffectsVerified,
	}
}
