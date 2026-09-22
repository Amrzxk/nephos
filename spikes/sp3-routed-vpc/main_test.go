package main

import "testing"

func TestShouldContinueAtomicReplacement(t *testing.T) {
	tests := []struct {
		name         string
		replacements int
		attempts     int
		want         bool
	}{
		{name: "replacement minimum not reached", replacements: 99, attempts: 50, want: true},
		{name: "probe minimum not reached", replacements: 100, attempts: 49, want: true},
		{name: "both minimums reached", replacements: 100, attempts: 50, want: false},
		{name: "replacement safety cap reached", replacements: 1000, attempts: 49, want: false},
		{name: "both minimums exceeded", replacements: 101, attempts: 51, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldContinueAtomicReplacement(tt.replacements, tt.attempts); got != tt.want {
				t.Fatalf("shouldContinueAtomicReplacement(%d, %d) = %t, want %t",
					tt.replacements, tt.attempts, got, tt.want)
			}
		})
	}
}
