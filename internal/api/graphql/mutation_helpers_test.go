package graphql

import (
	"errors"
	"testing"
)

func TestBoolToMutationResult(t *testing.T) {
	t.Run("true nil — success", func(t *testing.T) {
		got, err := boolToMutationResult(true, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Success {
			t.Error("expected Success=true")
		}
		if got.Error != nil {
			t.Errorf("expected Error=nil, got %q", *got.Error)
		}
	})

	t.Run("false nil — failed without error message", func(t *testing.T) {
		got, err := boolToMutationResult(false, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Success {
			t.Error("expected Success=false")
		}
		if got.Error != nil {
			t.Errorf("expected Error=nil, got %q", *got.Error)
		}
	})

	t.Run("false error — error message propagated", func(t *testing.T) {
		got, err := boolToMutationResult(false, errors.New("oops"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Success {
			t.Error("expected Success=false")
		}
		if got.Error == nil {
			t.Fatal("expected Error to be set")
		}
		if *got.Error != "oops" {
			t.Errorf("expected Error=%q, got %q", "oops", *got.Error)
		}
	})

	t.Run("true error — error takes precedence over ok", func(t *testing.T) {
		got, err := boolToMutationResult(true, errors.New("oops"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Success {
			t.Error("expected Success=false when err is non-nil")
		}
		if got.Error == nil {
			t.Fatal("expected Error to be set")
		}
		if *got.Error != "oops" {
			t.Errorf("expected Error=%q, got %q", "oops", *got.Error)
		}
	})
}
