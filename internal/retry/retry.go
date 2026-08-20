// Package retry provides exponential-backoff retry for transient failures.
package retry

import "time"

// Do runs fn up to attempts times with exponential backoff starting at
// baseDelay. When shouldRetry is non-nil and reports a permanent failure,
// Do returns immediately without sleeping (auth errors have no chance of
// succeeding on retry). A nil classifier retries everything. Returns the
// last error if all attempts fail.
func Do(attempts int, fn func() error, baseDelay time.Duration, shouldRetry func(error) bool) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if shouldRetry != nil && !shouldRetry(err) {
			return err
		}
		if i < attempts-1 {
			time.Sleep(baseDelay * time.Duration(1<<i))
		}
	}
	return err
}
