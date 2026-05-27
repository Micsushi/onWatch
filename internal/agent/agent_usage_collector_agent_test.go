package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
)

func TestAgentUsageCollectorAgentRunCollectsImmediately(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "claude.jsonl")
	if err := os.WriteFile(source, []byte(`{"timestamp":"2026-05-25T12:34:56Z","sessionId":"s1","requestId":"req_1","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":10}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	pricing, err := agentusage.DefaultPricingMap()
	if err != nil {
		t.Fatal(err)
	}
	ag := NewAgentUsageCollectorAgent(outDir, pricing, []agentusage.Source{
		{Kind: agentusage.SourceClaude, Path: source},
	}, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ag.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(outDir, "agent-usage-"+time.Now().UTC().Format("2006-01-02")+".jsonl")); err == nil {
			cancel()
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("collector did not write output")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
