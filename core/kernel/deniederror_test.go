package kernel

import (
	"errors"
	"fmt"
	"testing"
)

// TestDeniedError_Vocabulary locks the closed denial-with-feedback shape:
// askers return (ApprovalDeny, *DeniedError{Feedback}) so consuming modules
// can surface the human's reason without string matching.
func TestDeniedError_Vocabulary(t *testing.T) {
	d := &DeniedError{Feedback: "not now, the db is busy"}
	if d.Error() != "not now, the db is busy" {
		t.Fatalf("Error() = %q, want the feedback verbatim", d.Error())
	}
	if fb, ok := IsDeniedError(d); !ok || fb != "not now, the db is busy" {
		t.Fatalf("IsDeniedError = (%q, %v), want the feedback", fb, ok)
	}
	if _, ok := IsDeniedError(fmt.Errorf("wrapped: %w", d)); !ok {
		t.Fatal("IsDeniedError must see through wrapping")
	}
	if _, ok := IsDeniedError(errors.New("other")); ok {
		t.Fatal("unrelated errors must not classify as denials")
	}
}

// TestIsRetryableError_DeclaredPermanentModelError locks the C7 fix: a
// ModelError with Retryable=false is permanent BY DECLARATION and must not
// fall through to the default-true classification.
func TestIsRetryableError_DeclaredPermanentModelError(t *testing.T) {
	perm := &ModelError{Code: CodeModelError, Message: "auth", Retryable: false}
	if IsRetryableError(perm) {
		t.Fatal("a non-retryable model error must classify permanent")
	}
	if IsRetryableError(fmt.Errorf("wrapped: %w", perm)) {
		t.Fatal("wrapping must not change the declaration")
	}
	if !IsRetryableError(&ModelError{Code: CodeModelError, Retryable: true}) {
		t.Fatal("a retryable model error must classify retryable")
	}
	// The default-true classification for unclassified errors stays.
	if !IsRetryableError(errors.New("unknown transient")) {
		t.Fatal("unclassified errors keep the default-true classification")
	}
}
