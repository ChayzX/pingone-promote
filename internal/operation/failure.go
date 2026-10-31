package operation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

// Failure is the safe, stable projection of an operation failure. Cause is
// retained only for controlled local diagnostics and is never serialized or
// returned by Error.
type Failure struct {
	Code           string `json:"code"`
	Phase          string `json:"phase"`
	Message        string `json:"message"`
	RecoveryAction string `json:"recoveryAction"`
	CorrelationID  string `json:"correlationId"`
	cause          error
}

func NewFailure(code, phase, message, recoveryAction, correlationID string, cause error) *Failure {
	if correlationID == "" {
		correlationID = NewCorrelationID()
	}
	return &Failure{Code: code, Phase: phase, Message: message, RecoveryAction: recoveryAction, CorrelationID: correlationID, cause: cause}
}

func (f *Failure) Error() string {
	if f == nil || f.Message == "" {
		return "operation failed"
	}
	return f.Message
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

var correlationCounter atomic.Uint64

func NewCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("local-%x", correlationCounter.Add(1))
}
