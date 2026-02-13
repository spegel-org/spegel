package main

import (
	"context"
	"os"
	"testing"
)

func TestRunMain_NoArgs(t *testing.T) {
	t.Parallel()
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })
	os.Args = []string{"spegel"}

	exitCode := runMain()
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	t.Parallel()
	err := run(context.Background(), &Arguments{})
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}
