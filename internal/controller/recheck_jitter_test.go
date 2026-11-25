/*
Copyright 2025.

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

package controller

import (
	"os"
	"testing"
	"time"
)

// TestMain pins the TLS recheck jitter to its neutral value (0.5 -> no
// jitter) for every test in the package. Existing interval assertions across
// the suite are written against the un-jittered standing interval, and a
// random jitter would make them flaky. TestTLSRecheckJitter overrides the
// source explicitly to exercise the jitter bounds.
func TestMain(m *testing.M) {
	original := recheckJitterSource
	recheckJitterSource = func() float64 { return 0.5 }
	code := m.Run()
	recheckJitterSource = original
	os.Exit(code)
}

// TestTLSRecheckJitter covers the ±10% jitter applied to the standing TLS
// certificate recheck interval: it spreads rechecks (and renewals) of
// certificates created together across time instead of stampeding the API
// server and the certificate-generation path on the same reconcile tick.
func TestTLSRecheckJitter(t *testing.T) {
	originalInterval := tlsCertRecheckInterval
	originalThreshold := tlsCertRenewalThreshold
	originalSource := recheckJitterSource
	t.Cleanup(func() {
		tlsCertRecheckInterval = originalInterval
		tlsCertRenewalThreshold = originalThreshold
		recheckJitterSource = originalSource
	})

	tlsCertRecheckInterval = 12 * time.Hour
	tlsCertRenewalThreshold = 30 * 24 * time.Hour

	t.Run("bounds and neutral value", func(t *testing.T) {
		cases := []struct {
			name   string
			source float64
			want   time.Duration
		}{
			{name: "minimum jitter", source: 0, want: 10*time.Hour + 48*time.Minute}, // 12h * 0.9
			{name: "neutral jitter", source: 0.5, want: 12 * time.Hour},              // 12h * 1.0
			{name: "maximum jitter", source: 1, want: 13*time.Hour + 12*time.Minute}, // 12h * 1.1
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				recheckJitterSource = func() float64 { return tc.source }
				if got := jitteredRecheckInterval(tlsCertRecheckInterval); got != tc.want {
					t.Errorf("jitteredRecheckInterval(12h) with source %v = %v, want %v", tc.source, got, tc.want)
				}
			})
		}

		recheckJitterSource = func() float64 { return 1 }
		if got := jitteredRecheckInterval(0); got != 0 {
			t.Errorf("jitteredRecheckInterval(0) = %v, want 0", got)
		}
	})

	t.Run("shortened interval is not jittered", func(t *testing.T) {
		// The jitter source would stretch the standing interval to 13.2h, but
		// the certificate enters its renewal window in ~2h, so the helper
		// must return that (un-jittered) shorter value so the renewal is not
		// missed.
		recheckJitterSource = func() float64 { return 1 }
		notAfter := time.Now().Add(tlsCertRenewalThreshold + 2*time.Hour)
		got := certRecheckInterval(notAfter)
		if got > 2*time.Hour || got < 2*time.Hour-time.Minute {
			t.Errorf("certRecheckInterval with the window opening in 2h = %v, want the un-jittered ~2h", got)
		}
	})

	t.Run("standing interval is jittered", func(t *testing.T) {
		recheckJitterSource = func() float64 { return 1 }
		got := certRecheckInterval(time.Now().AddDate(1, 0, 0))
		want := 13*time.Hour + 12*time.Minute // 12h * 1.1
		if got != want {
			t.Errorf("certRecheckInterval far from expiry = %v, want the jittered standing interval %v", got, want)
		}
	})
}
