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

package main

import (
	"testing"
)

// TestWatchNamespace covers the B21 namespace-scoped mode entry point: the
// WATCH_NAMESPACE environment variable selects the namespace the manager
// restricts its cache to; unset, empty or whitespace-only values mean
// cluster-scoped mode (watch all namespaces).
func TestWatchNamespace(t *testing.T) {
	t.Run("unset means cluster-scoped", func(t *testing.T) {
		t.Setenv("WATCH_NAMESPACE", "")
		if got := watchNamespace(); got != "" {
			t.Errorf("expected an empty namespace for cluster-scoped mode, got %q", got)
		}
	})

	t.Run("set value is returned", func(t *testing.T) {
		t.Setenv("WATCH_NAMESPACE", "kafka")
		if got := watchNamespace(); got != "kafka" {
			t.Errorf("expected %q, got %q", "kafka", got)
		}
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		t.Setenv("WATCH_NAMESPACE", " kafka ")
		if got := watchNamespace(); got != "kafka" {
			t.Errorf("expected the trimmed namespace %q, got %q", "kafka", got)
		}
	})
}

// TestManagerCacheOptions covers the cache restriction built from the
// WATCH_NAMESPACE value: an empty namespace must leave the cache
// cluster-wide (zero options), a set namespace must produce exactly one
// DefaultNamespaces entry.
func TestManagerCacheOptions(t *testing.T) {
	t.Run("empty namespace keeps the cache cluster-wide", func(t *testing.T) {
		opts := managerCacheOptions("")
		if len(opts.DefaultNamespaces) != 0 {
			t.Errorf("expected no DefaultNamespaces restriction, got %v", opts.DefaultNamespaces)
		}
	})

	t.Run("namespace restricts the cache to a single entry", func(t *testing.T) {
		opts := managerCacheOptions("kafka")
		if len(opts.DefaultNamespaces) != 1 {
			t.Fatalf("expected exactly one DefaultNamespaces entry, got %v", opts.DefaultNamespaces)
		}
		if _, ok := opts.DefaultNamespaces["kafka"]; !ok {
			t.Errorf("expected the cache restricted to namespace %q, got %v", "kafka", opts.DefaultNamespaces)
		}
	})
}
