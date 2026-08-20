package retry

import (
	"errors"
	"testing"
)

func TestDoSuccessFirstTry(t *testing.T) {
	calls := 0
	err := Do(3, func() error {
		calls++
		return nil
	}, 0, nil)
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v, want 1 call and no error", calls, err)
	}
}

func TestDoPermanentErrorFailsFast(t *testing.T) {
	calls := 0
	err := Do(3, func() error {
		calls++
		return errors.New("boom")
	}, 0, func(error) bool { return false })
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("permanent error must not be retried, fn called %d times", calls)
	}
}

func TestDoTransientErrorRetries(t *testing.T) {
	calls := 0
	err := Do(3, func() error {
		calls++
		return errors.New("flaky")
	}, 0, func(error) bool { return true })
	if err == nil {
		t.Fatal("expected error after attempts exhausted")
	}
	if calls != 3 {
		t.Fatalf("transient error should be retried, fn called %d times", calls)
	}
}

func TestDoNilClassifierRetriesAll(t *testing.T) {
	calls := 0
	err := Do(2, func() error {
		calls++
		return errors.New("x")
	}, 0, nil)
	if err == nil || calls != 2 {
		t.Fatalf("nil classifier must retry everything: calls=%d err=%v", calls, err)
	}
}
