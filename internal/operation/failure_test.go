package operation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFailureErrorAndJSONExposeOnlySafeContract(t *testing.T) {
	f := NewFailure("execution_outcome_uncertain", "execution", "Reconcile native status before retrying.", "reconcile_native_status", "correlation-1", errors.New("vendor secret=hidden"))
	if f.Error() != "Reconcile native status before retrying." {
		t.Fatalf("Error() exposed cause or lost safe message: %q", f.Error())
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"code":"execution_outcome_uncertain"`, `"phase":"execution"`, `"recoveryAction":"reconcile_native_status"`, `"correlationId":"correlation-1"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("failure JSON omitted %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "vendor secret") || strings.Contains(string(b), "hidden") {
		t.Fatalf("failure JSON exposed diagnostic cause: %s", b)
	}
}

func TestNewFailureGeneratesCorrelationIDWhenAbsent(t *testing.T) {
	f := NewFailure("request_failed", "request", "Request failed.", "inspect_status", "", nil)
	if f.CorrelationID == "" {
		t.Fatal("failure omitted correlation ID")
	}
}
