// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import "testing"

func TestCoerceInt(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"nil", nil, 0},
		{"float64", float64(42), 42},
		{"float64 neg", float64(-7), -7},
		{"int", int(99), 99},
		{"int64", int64(1234), 1234},
		{"uint64", uint64(500), 500},
		{"bool (unsupported)", true, 0},
		{"string (unsupported)", "123", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coerceInt(tc.in); got != tc.want {
				t.Errorf("coerceInt(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestCoerceUint64(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want uint64
	}{
		{"nil", nil, 0},
		{"float64 pos", float64(42), 42},
		{"float64 neg", float64(-1), 0},
		{"uint64", uint64(9999), 9999},
		{"int pos", int(100), 100},
		{"int neg", int(-5), 0},
		{"int64 pos", int64(256), 256},
		{"int64 neg", int64(-3), 0},
		{"bool (unsupported)", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coerceUint64(tc.in); got != tc.want {
				t.Errorf("coerceUint64(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
