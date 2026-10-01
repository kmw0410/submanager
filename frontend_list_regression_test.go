package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestFrontendSubscriptionListDecisionsAndMarkup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("Node is required in CI for frontend behavior regression checks")
		}
		t.Skip("Node is optional for local Go-only checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "scripts/frontend-list-test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("frontend list regression check failed: %v\n%s", err, output)
	}
}
