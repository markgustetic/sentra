package main

import (
	"context"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/config"
)

func defaultHeuristics() []heuristics.Heuristic {
	return []heuristics.Heuristic{
		heuristics.NewSecrets(),
		heuristics.NewLargeFiles(),
		heuristics.NewCacheDirs(),
		heuristics.NewStalePaths(),
		heuristics.NewDupPaths(),
		heuristics.NewOrphanBlobs(),
		heuristics.NewRetentionDrift(),
	}
}

// newAgentProvider returns the production LLM provider. Missing API-key
// failures are deferred until agent scan actually calls Generate so unrelated
// commands can still start normally.
func newAgentProvider(cfg *config.Config) llm.Provider {
	model := "claude-sonnet-4-6"
	if cfg != nil && cfg.Agent.Model != "" {
		model = cfg.Agent.Model
	}
	p, err := llm.NewAnthropic(llm.AnthropicConfig{Model: model})
	if err != nil {
		return &lazyErrProvider{err: err}
	}
	return p
}

type lazyErrProvider struct{ err error }

func (p *lazyErrProvider) Generate(context.Context, string, []llm.Message, []llm.Tool, chan<- string) ([]llm.ToolCall, string, error) {
	return nil, "", p.err
}
