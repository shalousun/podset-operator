/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"testing"
	"time"

	dataclondv1 "podset-operator/api/v1"
)

func TestTokenBucketRateLimit(t *testing.T) {
	store := newTokenBucketStore()
	spec := &dataclondv1.PodLoadTestSpec{SubmitRatePerSecond: 10, SubmitBurst: 10}
	now := time.Unix(1000, 0)

	if got := store.take("run", spec, now, 100); got != 10 {
		t.Fatalf("initial burst: got %d, want 10", got)
	}
	if got := store.take("run", spec, now, 100); got != 0 {
		t.Fatalf("tokens should be exhausted: got %d, want 0", got)
	}
	if got := store.take("run", spec, now.Add(500*time.Millisecond), 100); got != 5 {
		t.Fatalf("after 500ms at 10/s: got %d, want 5", got)
	}
	if got := store.take("run", spec, now.Add(2*time.Second), 100); got != 10 {
		t.Fatalf("refill must be capped at burst: got %d, want 10", got)
	}
}

func TestTokenBucketUnlimited(t *testing.T) {
	store := newTokenBucketStore()
	spec := &dataclondv1.PodLoadTestSpec{}
	if got := store.take("run", spec, time.Unix(1000, 0), 123); got != 123 {
		t.Fatalf("unlimited rate: got %d, want 123", got)
	}
}

func TestRateWindow(t *testing.T) {
	store := newRateWindowStore()
	now := time.Unix(1000, 0)

	if got := store.recordAndRate("run", now, 10, time.Second); got != 10 {
		t.Fatalf("rate after 10 events in a 1s window: got %f, want 10", got)
	}
	if got := store.recordAndRate("run", now.Add(2*time.Second), 0, time.Second); got != 0 {
		t.Fatalf("expired events: got %f, want 0", got)
	}
}
