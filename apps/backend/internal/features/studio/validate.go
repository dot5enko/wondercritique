// Per-kind body validation (draft save, publish) and pipeline graph validation.
package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// reasoningEfforts are the accepted values of ModelParams.ReasoningEffort.
var reasoningEfforts = map[string]bool{"minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

// severities are the accepted rule severities.
var severities = map[string]bool{"info": true, "warning": true, "error": true}

// maxSkillFileBytes caps one skill file's content (512 KiB).
const maxSkillFileBytes = 512 << 10

// validateForDraft validates a typed body the way a draft PUT must: full
// validation for model_set, agent, skill and rule; structural decode only for
// pipeline (the editor autosaves intermediate graph states).
func validateForDraft(kind string, body any) error {
	switch kind {
	case KindModelSet:
		// Drafts may have zero entries (a fresh set is created empty and
		// filled in the editor); publish requires at least one.
		return validateModelSet(body.(ModelSetBody), false)
	case KindAgent:
		return validateAgentDraft(body.(AgentBody))
	case KindSkill:
		return validateSkill(body.(SkillBody))
	case KindRule:
		return validateRule(body.(RuleBody), false)
	case KindPipeline:
		return nil // structural decode only
	}
	return apperr.Invalid("unknown kind", nil)
}

// validateForPublish validates a typed body fully; used on publish and import.
func validateForPublish(kind string, body any) error {
	switch kind {
	case KindModelSet:
		return validateModelSet(body.(ModelSetBody), true)
	case KindAgent:
		return validateAgentDraft(body.(AgentBody))
	case KindSkill:
		return validateSkill(body.(SkillBody))
	case KindRule:
		return validateRule(body.(RuleBody), true)
	case KindPipeline:
		return nil // graph validation needs the catalog; callers use ValidatePipelineBody
	}
	return apperr.Invalid("unknown kind", nil)
}

func invalid(msg string) *apperr.Error { return apperr.Invalid(msg, nil) }

func invalidf(format string, args ...any) *apperr.Error {
	return apperr.Invalid(fmt.Sprintf(format, args...), nil)
}

/* ---------- model_set ---------- */

// validateModelSet checks a model-set body. requireEntries is false for draft
// saves (an empty set is a valid work-in-progress) and true for publish/import.
func validateModelSet(b ModelSetBody, requireEntries bool) error {
	switch b.Strategy {
	case "single", "fallback", "fanout":
	default:
		return invalidf("strategy %q must be single, fallback or fanout", b.Strategy)
	}
	if requireEntries && len(b.Entries) == 0 {
		return invalid("add at least one model before publishing")
	}
	for i, e := range b.Entries {
		if e.ProviderID == "" {
			return invalidf("entry %d: provider_id is required", i)
		}
		if e.ModelID == "" {
			return invalidf("entry %d: model_id is required", i)
		}
		if e.Params != nil {
			if e.Params.ReasoningEffort != "" && !reasoningEfforts[e.Params.ReasoningEffort] {
				return invalidf("entry %d: invalid reasoning_effort %q", i, e.Params.ReasoningEffort)
			}
			if e.Params.MaxOutputTokens < 0 {
				return invalidf("entry %d: max_output_tokens must be >= 0", i)
			}
		}
	}
	return nil
}

/* ---------- agent ---------- */

func validateAgentDraft(b AgentBody) error {
	if b.ModelSetID == "" {
		return invalid("model_set_id is required")
	}
	switch b.OutputContract.Type {
	case "artifact", "critique":
	case "custom":
		if len(b.OutputContract.FieldsSchema) == 0 {
			return invalid("output contract custom requires fields_schema")
		}
		if err := validateFieldsSchema(b.OutputContract.FieldsSchema); err != nil {
			return err
		}
	default:
		return invalidf("output contract type %q must be artifact, critique or custom", b.OutputContract.Type)
	}
	if b.Limits.MaxSteps < 0 || b.Limits.MaxIterations < 0 {
		return invalid("limits must be >= 0")
	}
	for _, tool := range b.Tools {
		if !IsCatalogTool(tool) {
			return invalidf("unknown tool %q", tool)
		}
	}
	return nil
}

// validateFieldsSchema requires a JSON object schema with a "properties" object.
func validateFieldsSchema(schema json.RawMessage) error {
	var s struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return invalid("fields_schema must be a JSON object").Wrap(err)
	}
	if s.Type != "" && s.Type != "object" {
		return invalidf("fields_schema type must be \"object\", got %q", s.Type)
	}
	var props map[string]any
	if len(s.Properties) == 0 || json.Unmarshal(s.Properties, &props) != nil || len(props) == 0 {
		return invalid("fields_schema must have a non-empty properties object")
	}
	return nil
}

/* ---------- skill ---------- */

func validateSkill(b SkillBody) error {
	if len(b.Files) == 0 {
		return invalid("at least one file is required")
	}
	seen := map[string]bool{}
	skillMD := 0
	for _, f := range b.Files {
		if err := validateSkillPath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return invalidf("duplicate file path %q", f.Path)
		}
		seen[f.Path] = true
		if f.Path == SKILLFileName {
			skillMD++
		}
		if len(f.Content) > maxSkillFileBytes {
			return invalidf("file %q is %d bytes; the limit is %d bytes", f.Path, len(f.Content), maxSkillFileBytes)
		}
	}
	if skillMD != 1 {
		return invalid("exactly one SKILL.md file is required")
	}
	return nil
}

// validateSkillPath checks a skill file path with vfs.Clean semantics
// (relative form): no leading "/", no "." or ".." segments, no empty segments,
// no backslash/control characters, valid UTF-8.
func validateSkillPath(p string) error {
	if p == "" {
		return invalid("file path is empty")
	}
	if p[0] == '/' {
		return invalidf("file path %q must be relative", p)
	}
	if !utf8.ValidString(p) {
		return invalidf("file path %q is not valid UTF-8", p)
	}
	for _, r := range p {
		if r == '\\' || (r < 0x20 && r != '\t') {
			return invalidf("file path %q contains a backslash or control character", p)
		}
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return invalidf("file path %q contains an empty segment", p)
		case ".", "..":
			return invalidf(`file path %q contains %q`, p, seg)
		}
	}
	return nil
}

/* ---------- rule ---------- */

// validateRule checks a rule-set body. requireRules is false for draft saves
// (a fresh set starts with an empty group) and true for publish/import.
func validateRule(b RuleBody, requireRules bool) error {
	if len(b.Groups) == 0 {
		return invalid("at least one group is required")
	}
	for gi, g := range b.Groups {
		if g.Name == "" {
			return invalidf("group %d: name is required", gi)
		}
		if requireRules && len(g.Rules) == 0 {
			return invalidf("group %q: at least one rule is required", g.Name)
		}
		for ri, r := range g.Rules {
			if r.Text == "" {
				return invalidf("group %q rule %d: text is required", g.Name, ri)
			}
			if !severities[r.Severity] {
				return invalidf("group %q rule %d: severity %q must be info, warning or error", g.Name, ri, r.Severity)
			}
		}
	}
	return nil
}

/* ---------- pipeline ---------- */

// PipelineIssue is one pipeline validation finding, referencing a node id.
type PipelineIssue struct {
	NodeID   string `json:"node_id"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error | warning
}

// ValidatePipelineBody validates a pipeline body against the catalog: unique
// node ids, existing nodes and ports on every edge, port-type compatibility,
// at most one loop edge (only from a Gate `continue` output), a DAG of
// non-loop edges, and agent-node references. Issues reference node ids.
func ValidatePipelineBody(ctx context.Context, body PipelineBody, cat *CatalogData) []PipelineIssue {
	var issues []PipelineIssue
	add := func(nodeID, msg, sev string) {
		issues = append(issues, PipelineIssue{NodeID: nodeID, Message: msg, Severity: sev})
	}

	if len(body.Nodes) == 0 {
		add("", "pipeline needs at least one node", "error")
		return issues
	}

	nodes := map[string]PipelineNode{}
	for _, n := range body.Nodes {
		if n.ID == "" {
			add("", "node id is required", "error")
			continue
		}
		if _, dup := nodes[n.ID]; dup {
			add(n.ID, "duplicate node id", "error")
			continue
		}
		nodes[n.ID] = n
		nt := cat.NodeType(n.Type)
		if nt == nil {
			add(n.ID, fmt.Sprintf("unknown node type %q", n.Type), "error")
			continue
		}
		if n.Type == "agent" {
			if _, ok := n.Config["agent_id"].(string); !ok || n.Config["agent_id"].(string) == "" {
				add(n.ID, "agent node needs an agent_id", "warning")
			} else if agentID, _ := n.Config["agent_id"].(string); !cat.hasAgent(agentID) {
				add(n.ID, fmt.Sprintf("agent %q does not exist", agentID), "warning")
			}
		}
	}

	loops := 0
	for _, e := range body.Edges {
		from, okFrom := nodes[e.From.Node]
		to, okTo := nodes[e.To.Node]
		if !okFrom {
			add(e.From.Node, fmt.Sprintf("edge %q references unknown node", e.ID), "error")
			continue
		}
		if !okTo {
			add(e.To.Node, fmt.Sprintf("edge %q references unknown node", e.ID), "error")
			continue
		}
		fromType, toType := cat.NodeType(from.Type), cat.NodeType(to.Type)
		if fromType == nil || toType == nil {
			continue // node type already reported
		}
		outPort := fromType.OutputPort(e.From.Port)
		inPort := toType.InputPort(e.To.Port)
		if outPort == nil {
			add(from.ID, fmt.Sprintf("no output port %q (edge %q)", e.From.Port, e.ID), "error")
			continue
		}
		if inPort == nil {
			add(to.ID, fmt.Sprintf("no input port %q (edge %q)", e.To.Port, e.ID), "error")
			continue
		}
		if outPort.Type != inPort.Type {
			add(from.ID, fmt.Sprintf("edge %q: port type %s does not match %s", e.ID, outPort.Type, inPort.Type), "error")
			continue
		}
		if e.Loop {
			loops++
		}
	}
	// The loop-edge source rule is checked regardless of port resolution.
	for _, e := range body.Edges {
		if !e.Loop {
			continue
		}
		from, ok := nodes[e.From.Node]
		if !ok {
			continue // already reported
		}
		if from.Type != "gate" || e.From.Port != "continue" {
			add(from.ID, fmt.Sprintf("loop edge %q must leave a gate's continue output", e.ID), "error")
		}
	}
	if loops > 1 {
		add("", fmt.Sprintf("%d loop edges; at most one is allowed", loops), "error")
	}

	// The non-loop edges must form a DAG (DFS cycle detection).
	if cyclic := cycleNode(body, nodes); cyclic != "" {
		add(cyclic, "the graph (excluding the loop edge) must be a DAG: cycle detected", "error")
	}
	return issues
}

// cycleNode returns a node id on a cycle of the non-loop edge graph ("" when acyclic).
func cycleNode(body PipelineBody, nodes map[string]PipelineNode) string {
	adj := map[string][]string{}
	for _, e := range body.Edges {
		if e.Loop {
			continue
		}
		if _, ok := nodes[e.From.Node]; !ok {
			continue
		}
		if _, ok := nodes[e.To.Node]; !ok {
			continue
		}
		adj[e.From.Node] = append(adj[e.From.Node], e.To.Node)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var onCycle string
	var dfs func(string) bool
	dfs = func(id string) bool {
		color[id] = gray
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				onCycle = next
				return true
			case white:
				if dfs(next) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}
	for _, n := range body.Nodes {
		if color[n.ID] == white && dfs(n.ID) {
			return onCycle
		}
	}
	return ""
}
