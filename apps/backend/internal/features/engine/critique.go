package engine

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
)

// formatCritique renders a critique as markdown for agents that consume it
// (previous-critique feedback, the next generator iteration).
func formatCritique(c *agentrun.Critique) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Score: %s/10\n", trimFloat(c.Score))
	if len(c.Strengths) > 0 {
		b.WriteString("\nStrengths:\n")
		for _, s := range c.Strengths {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if len(c.Issues) > 0 {
		b.WriteString("\nIssues:\n")
		for _, it := range c.Issues {
			fmt.Fprintf(&b, "- [%s]", it.Severity)
			if it.Code != "" {
				fmt.Fprintf(&b, " (%s)", it.Code)
			}
			fmt.Fprintf(&b, " %s", it.Message)
			if it.Suggestion != "" {
				fmt.Fprintf(&b, " — Fix: %s", it.Suggestion)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// trimFloat formats a score with at most 2 decimals.
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

func severityRank(s string) int {
	switch s {
	case "error":
		return 3
	case "warning":
		return 2
	}
	return 1
}

// mergeCritiques combines several critiques: the score is the mean, strengths
// are de-duplicated, and issues are concatenated (de-duplicated by code +
// message when dedupe is set, keeping the highest severity) and ordered by
// severity.
func mergeCritiques(cs []*agentrun.Critique, dedupe bool) *agentrun.Critique {
	out := &agentrun.Critique{Strengths: []string{}, Issues: []agentrun.CritiqueIssue{}}
	if len(cs) == 0 {
		return out
	}
	seenStrength := map[string]bool{}
	issueAt := map[string]int{}
	var sum float64
	for _, c := range cs {
		sum += c.Score
		for _, s := range c.Strengths {
			k := strings.ToLower(strings.TrimSpace(s))
			if k == "" || (dedupe && seenStrength[k]) {
				continue
			}
			seenStrength[k] = true
			out.Strengths = append(out.Strengths, s)
		}
		for _, it := range c.Issues {
			if dedupe {
				k := strings.ToLower(strings.TrimSpace(it.Code)) + "|" + strings.ToLower(strings.TrimSpace(it.Message))
				if i, ok := issueAt[k]; ok {
					ex := &out.Issues[i]
					if severityRank(it.Severity) > severityRank(ex.Severity) {
						ex.Severity = it.Severity
					}
					if ex.Suggestion == "" {
						ex.Suggestion = it.Suggestion
					}
					continue
				}
				issueAt[k] = len(out.Issues)
			}
			out.Issues = append(out.Issues, it)
		}
	}
	out.Score = math.Round(sum/float64(len(cs))*100) / 100
	sort.SliceStable(out.Issues, func(i, j int) bool {
		return severityRank(out.Issues[i].Severity) > severityRank(out.Issues[j].Severity)
	})
	return out
}

/* ---------- gate ---------- */

// DefaultMinScore is the gate's target when min_score is not configured.
const DefaultMinScore = 8.0

// maxIterationsCap bounds any pipeline's loop regardless of gate settings.
const maxIterationsCap = 10

type gateConfig struct {
	MinScore      float64
	MaxIterations int
	Patience      int
	BudgetUSD     float64
}

func parseGateConfig(cfg map[string]any) gateConfig {
	g := gateConfig{
		MinScore:      numCfg(cfg, "min_score", 0),
		MaxIterations: int(numCfg(cfg, "max_iterations", 3)),
		Patience:      int(numCfg(cfg, "patience", 1)),
		BudgetUSD:     numCfg(cfg, "budget_usd", 0),
	}
	if g.MinScore <= 0 {
		g.MinScore = DefaultMinScore
	}
	if g.MaxIterations <= 0 {
		g.MaxIterations = 3
	}
	g.MaxIterations = min(g.MaxIterations, maxIterationsCap)
	if g.Patience < 0 {
		g.Patience = 0
	}
	return g
}

type gateInput struct {
	Score     float64
	Iteration int
	NoImprove int // consecutive iterations (including this one) without a better score
	Cost      float64
	CostKnown bool
	HasLoop   bool
}

// decideGate returns whether the pipeline should iterate again, with the reason.
func decideGate(cfg gateConfig, in gateInput) (cont bool, reason string) {
	switch {
	case in.Score >= cfg.MinScore:
		return false, fmt.Sprintf("score %s reached the target %s", trimFloat(in.Score), trimFloat(cfg.MinScore))
	case !in.HasLoop:
		return false, "the pipeline has no loop edge to iterate on"
	case in.Iteration >= cfg.MaxIterations:
		return false, fmt.Sprintf("reached the maximum of %d iterations", cfg.MaxIterations)
	case cfg.Patience > 0 && in.NoImprove >= cfg.Patience:
		return false, fmt.Sprintf("no improvement for %d iteration(s)", in.NoImprove)
	case cfg.BudgetUSD > 0 && in.CostKnown && in.Cost >= cfg.BudgetUSD:
		return false, fmt.Sprintf("budget of $%s reached", trimFloat(cfg.BudgetUSD))
	}
	return true, fmt.Sprintf("score %s is below the target %s", trimFloat(in.Score), trimFloat(cfg.MinScore))
}

/* ---------- config helpers ---------- */

func numCfg(cfg map[string]any, key string, def float64) float64 {
	switch v := cfg[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

func strCfg(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// strListCfg reads a list of non-empty strings (blank entries keep their slot
// as "" so indexes stay aligned).
func strListCfg(cfg map[string]any, key string) []string {
	list, ok := cfg[key].([]any)
	if !ok {
		if ss, ok := cfg[key].([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, _ := v.(string)
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func boolCfg(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key].(bool); ok {
		return v
	}
	return def
}
