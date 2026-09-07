package domain

import "testing"

func TestOperationTransitions(t *testing.T) {
	tests := []struct {
		from OperationState
		to   OperationState
		want bool
	}{
		{OperationQueued, OperationRunning, true},
		{OperationRunning, OperationQueued, true},
		{OperationRunning, OperationSucceeded, true},
		{OperationSucceeded, OperationRunning, false},
		{OperationCancelled, OperationQueued, false},
		{OperationPaused, OperationSucceeded, false},
	}
	for _, tt := range tests {
		if got := CanTransitionOperation(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransitionOperation(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}
