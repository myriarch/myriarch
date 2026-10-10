// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package authn

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Check and use of the account backoff are one step: of any number of
// concurrent attempts, one gets the verification slot.
func TestAccountBackoffHandsOutOneSlot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := newAccountBackoff(func() time.Time { return now })

	var granted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, ok := b.acquire(); ok {
				granted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if granted.Load() != 1 {
		t.Fatalf("%d slots granted, want 1", granted.Load())
	}

	// a refused attempt reports a wait, and the slot is free again after release
	if retry, _, ok := b.acquire(); ok || retry <= 0 {
		t.Fatalf("second acquire: ok=%v retry=%v", ok, retry)
	}
	b.release()
	if _, _, ok := b.acquire(); !ok {
		t.Fatal("the slot must be free after release")
	}
	b.release()

	// failures beyond the grace count start the delay
	for i := 0; i <= bootstrapFailGrace; i++ {
		b.fail()
	}
	if retry, _, ok := b.acquire(); ok || retry <= 0 {
		t.Fatalf("after %d failures: ok=%v retry=%v", bootstrapFailGrace+1, ok, retry)
	}
}
