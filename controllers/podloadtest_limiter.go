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
	"math"
	"sync"
	"time"

	dataclondv1 "podset-operator/api/v1"
)

// tokenBucketStore keeps one token bucket per PodLoadTest run. The bucket
// implements the client-side submission rate limit.
type tokenBucketStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucketStore() *tokenBucketStore {
	return &tokenBucketStore{buckets: map[string]*tokenBucket{}}
}

// take returns how many Pods may be submitted now, consuming that many tokens.
// A rate of zero means unlimited submission.
func (s *tokenBucketStore) take(key string, spec *dataclondv1.PodLoadTestSpec, now time.Time, n int) int {
	if n <= 0 {
		return 0
	}
	rate := float64(spec.SubmitRatePerSecond)
	if rate <= 0 {
		return n
	}
	burst := float64(spec.SubmitBurst)
	if burst <= 0 {
		burst = math.Max(1, rate)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.buckets[key]
	if b == nil {
		b = &tokenBucket{rate: rate, burst: burst, tokens: burst, last: now}
		s.buckets[key] = b
	} else {
		if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
			b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
		}
		b.last = now
		if b.rate != rate || b.burst != burst {
			b.rate = rate
			b.burst = burst
			if b.tokens > burst {
				b.tokens = burst
			}
		}
	}

	take := int(math.Floor(b.tokens))
	if take > n {
		take = n
	}
	if take < 0 {
		take = 0
	}
	b.tokens -= float64(take)
	return take
}

func (s *tokenBucketStore) forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.buckets, key)
}

// rateWindowStore keeps a sliding window of submission timestamps per run and
// reports the observed submission rate.
type rateWindowStore struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
}

type rateWindow struct {
	events []time.Time
}

func newRateWindowStore() *rateWindowStore {
	return &rateWindowStore{windows: map[string]*rateWindow{}}
}

// recordAndRate records n submissions at now and returns the observed rate
// over the given window.
func (s *rateWindowStore) recordAndRate(key string, now time.Time, n int, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	w := s.windows[key]
	if w == nil {
		w = &rateWindow{}
		s.windows[key] = w
	}
	for i := 0; i < n; i++ {
		w.events = append(w.events, now)
	}

	cutoff := now.Add(-window)
	expired := 0
	for expired < len(w.events) && w.events[expired].Before(cutoff) {
		expired++
	}
	if expired > 0 {
		w.events = append([]time.Time(nil), w.events[expired:]...)
	}
	if len(w.events) == 0 {
		return 0
	}
	return float64(len(w.events)) / window.Seconds()
}

func (s *rateWindowStore) forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.windows, key)
}
