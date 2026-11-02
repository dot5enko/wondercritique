package studio

import (
	"encoding/json"
)

// Definition kinds. The kind string is used verbatim in API path segments:
// `/studio/model_set`, `/studio/agent`, … (singular), while action routes use
// the plural forms the frontend calls (`/studio/agents/{id}/try`, …).
const (
	KindModelSet = "model_set"
	KindAgent    = "agent"
	KindPipeline = "pipeline"
	KindSkill    = "skill"
	KindRule     = "rule"
)

// Kinds lists every definition kind.
var Kinds = []string{KindModelSet, KindAgent, KindPipeline, KindSkill, KindRule}

// ValidKind reports whether k is a known definition kind.
func ValidKind(k string) bool {
	switch k {
	case KindModelSet, KindAgent, KindPipeline, KindSkill, KindRule:
		return true
	}
	return false
}

/* ---------- model_set ---------- */

// ModelParams are per-entry model parameters (plan §3.4).
type ModelParams struct {
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // one of the model's reasoning_options
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

// ModelSetEntry references one model from an enabled provider.
type ModelSetEntry struct {
	ProviderID string       `json:"provider_id"`
	ModelID    string       `json:"model_id"`
	Params     *ModelParams `json:"params,omitempty"`
}

// ModelSetBody is the body of a model_set definition.
type ModelSetBody struct {
	Strategy string          `json:"strategy"` // single | fallback | fanout
	Entries  []ModelSetEntry `json:"entries"`
}

/* ---------- agent ---------- */

// OutputContract declares what an agent must produce (plan §4.4).
type OutputContract struct {
	Type         string          `json:"type"` // artifact | critique | custom
	FieldsSchema json.RawMessage `json:"fields_schema,omitempty"`
}

// AgentLimits cap the agent's tool loop and pipeline iterations.
type AgentLimits struct {
	MaxSteps      int `json:"max_steps,omitempty"`      // agentkit MaxSteps (default 20)
	MaxIterations int `json:"max_iterations,omitempty"` // pipeline gate hint
}

// AgentBody is the body of an agent definition (plan §4.3–4.4).
type AgentBody struct {
	ModelSetID     string         `json:"model_set_id"`
	SystemPrompt   string         `json:"system_prompt"`
	InputTemplate  string         `json:"input_template"`
	Tools          []string       `json:"tools"`
	SkillIDs       []string       `json:"skill_ids"`
	RuleIDs        []string       `json:"rule_ids"`
	OutputContract OutputContract `json:"output_contract"`
	Limits         AgentLimits    `json:"limits"`
}

/* ---------- pipeline ---------- */

// NodePosition is the editor position of a pipeline node.
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PipelineNode is one node of the pipeline graph (plan §4.5).
type PipelineNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"` // one of the catalog node types
	Config   map[string]any `json:"config"`
	Position NodePosition   `json:"position"`
}

// EdgeEndpoint is one end of a pipeline wire.
type EdgeEndpoint struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

// PipelineEdge is one wire of the pipeline graph. Loop marks the single
// Gate `continue` feedback edge; every other arrangement must form a DAG.
type PipelineEdge struct {
	ID   string       `json:"id"`
	From EdgeEndpoint `json:"from"`
	To   EdgeEndpoint `json:"to"`
	Loop bool         `json:"loop,omitempty"`
}

// PipelineBody is the body of a pipeline definition.
type PipelineBody struct {
	Nodes []PipelineNode `json:"nodes"`
	Edges []PipelineEdge `json:"edges"`
}

/* ---------- skill ---------- */

// SkillFile is one file of a skill, stored inline (plan §4.6).
type SkillFile struct {
	Path    string `json:"path"` // relative to the skill root, e.g. SKILL.md
	Content string `json:"content"`
}

// SkillBody is the body of a skill definition.
type SkillBody struct {
	Files []SkillFile `json:"files"`
}

// SKILLFileName is the manifest file every skill must have.
const SKILLFileName = "SKILL.md"

/* ---------- rule ---------- */

// RuleItem is a single rule with an optional validator code.
type RuleItem struct {
	Text          string `json:"text"`
	Severity      string `json:"severity"` // info | warning | error
	ValidatorCode string `json:"validator_code,omitempty"`
}

// RuleGroup is a named group of rules.
type RuleGroup struct {
	Name  string     `json:"name"`
	Rules []RuleItem `json:"rules"`
}

// RuleBody is the body of a rule definition.
type RuleBody struct {
	Groups []RuleGroup `json:"groups"`
}
