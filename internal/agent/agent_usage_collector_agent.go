package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/agentusage"
)

const (
	agentUsageCollectorIntervalDefault = 15 * time.Second
	agentUsageCollectorIntervalMax     = 15 * time.Second
)

type AgentUsageCollectorAgent struct {
	collector *agentusage.Collector
	interval  time.Duration
	logger    *slog.Logger
}

func NewAgentUsageCollectorAgent(outDir string, pricing *agentusage.PricingMap, sources []agentusage.Source, interval time.Duration, logger *slog.Logger) *AgentUsageCollectorAgent {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = agentUsageCollectorIntervalDefault
	}
	if interval > agentUsageCollectorIntervalMax {
		interval = agentUsageCollectorIntervalMax
	}
	return &AgentUsageCollectorAgent{
		collector: agentusage.NewCollector(outDir, pricing, sources, logger),
		interval:  interval,
		logger:    logger,
	}
}

func (a *AgentUsageCollectorAgent) Run(ctx context.Context) error {
	a.logger.Info("Agent usage collector started", "interval", a.interval)
	defer a.logger.Info("Agent usage collector stopped")

	if err := a.collector.CollectOnce(); err != nil {
		a.logger.Warn("Agent usage collector scan failed", "error", err)
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := a.collector.CollectOnce(); err != nil {
				a.logger.Warn("Agent usage collector scan failed", "error", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}
