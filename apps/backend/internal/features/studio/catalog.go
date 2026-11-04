// The studio catalog: node types, port types, tools, models, pickers and
// prompt metadata for the editors (plan §4.2).
package studio

import (
	"context"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/component"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/llm"
)

/* ---------- catalog shapes ---------- */

// PortType is a named wire type.
type PortType struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// NodePort is a named port on a node type.
type NodePort struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Multiple bool   `json:"multiple,omitempty"`
}

// NodeType describes one pipeline node type.
type NodeType struct {
	Type         string         `json:"type"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Inputs       []NodePort     `json:"inputs"`
	Outputs      []NodePort     `json:"outputs"`
	ConfigSchema map[string]any `json:"config_schema"`
	Defaults     map[string]any `json:"defaults,omitempty"`
}

// ToolInfo is a tool agents may use.
type ToolInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Flags       []string `json:"flags"`
}

// CatalogModel is one model from an enabled provider.
type CatalogModel struct {
	ProviderID   string           `json:"provider_id"`
	ModelID      string           `json:"model_id"`
	DisplayName  string           `json:"display_name"`
	Capabilities CatalogModelCaps `json:"capabilities"`
	Enabled      bool             `json:"enabled"`
}

// CatalogModelCaps mirrors models.dev capabilities.
type CatalogModelCaps struct {
	Vision           bool     `json:"vision"`
	Tools            bool     `json:"tools"`
	ReasoningOptions []string `json:"reasoning_options"`
	ContextWindow    int      `json:"context_window"`
	MaxOutput        int      `json:"max_output"`
}

// StudioRef references another definition for pickers (all defs, drafts included).
type StudioRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// PromptSection is an auto-appended prompt section.
type PromptSection struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// PromptVariable is a {{name}} placeholder usable in prompts/templates.
type PromptVariable struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CatalogData is everything the catalog endpoint serves; it is also the input
// of ValidatePipelineBody (Agents also feed the tool_config pickers).
type CatalogData struct {
	PortTypes          []PortType       `json:"port_types"`
	NodeTypes          []NodeType       `json:"node_types"`
	Tools              []ToolInfo       `json:"tools"`
	Models             []CatalogModel   `json:"models"`
	ModelSets          []StudioRef      `json:"model_sets"`
	Skills             []StudioRef      `json:"skills"`
	Rules              []StudioRef      `json:"rules"`
	PromptSections     []PromptSection  `json:"prompt_sections"`
	PromptVariables    []PromptVariable `json:"prompt_variables"`
	ValidatorRuleCodes []string         `json:"validator_rule_codes"`

	Agents []StudioRef `json:"agents"` // pipeline validation + tool_config / judge pickers
}

// NodeType returns the node type with the given id (nil when unknown).
func (c *CatalogData) NodeType(id string) *NodeType {
	for i := range c.NodeTypes {
		if c.NodeTypes[i].Type == id {
			return &c.NodeTypes[i]
		}
	}
	return nil
}

// OutputPort returns the named output port of a node type (nil when absent).
func (t *NodeType) OutputPort(name string) *NodePort {
	for i := range t.Outputs {
		if t.Outputs[i].Name == name {
			return &t.Outputs[i]
		}
	}
	return nil
}

// InputPort returns the named input port of a node type (nil when absent).
func (t *NodeType) InputPort(name string) *NodePort {
	for i := range t.Inputs {
		if t.Inputs[i].Name == name {
			return &t.Inputs[i]
		}
	}
	return nil
}

func (c *CatalogData) hasAgent(id string) bool {
	for _, a := range c.Agents {
		if a.ID == id {
			return true
		}
	}
	return false
}

/* ---------- catalog content ---------- */

// catalogPortTypes lists the wire types.
func catalogPortTypes() []PortType {
	return []PortType{
		{"text", "Text"},
		{"image", "Image"},
		{"critique", "Critique"},
		{"json", "JSON"},
		{"candidates", "Candidates"},
	}
}

// strListSchema is a JSON schema for an array of strings.
func strListSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

// catalogNodeTypes builds the node type list. Config schemas are JSON Schema
// objects the SchemaForm renders; defaults seed new nodes.
func catalogNodeTypes() []NodeType {
	return []NodeType{
		{
			Type:        "input",
			Name:        "Input",
			Description: "Collects the run inputs: a brief, references and custom fields.",
			Outputs: []NodePort{
				{Name: "brief", Type: "text"},
				{Name: "references", Type: "image", Multiple: true},
			},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label": map[string]any{"type": "string", "title": "Label"},
					"fields": map[string]any{
						"type": "array", "title": "Form fields",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"key":      map[string]any{"type": "string", "title": "Key"},
								"label":    map[string]any{"type": "string", "title": "Label"},
								"type":     map[string]any{"type": "string", "title": "Type", "enum": []string{"text", "textarea", "image"}},
								"required": map[string]any{"type": "boolean", "title": "Required", "default": true},
							},
							"required": []string{"key", "label", "type"},
						},
					},
				},
			},
			Defaults: map[string]any{
				"label": "Input",
				"fields": []map[string]any{
					{"key": "brief", "label": "Brief", "type": "textarea", "required": true},
				},
			},
		},
		{
			Type:        "agent",
			Name:        "Agent",
			Description: "Runs a studio agent. Real ports come from the selected agent; these are the generic superset.",
			Inputs: []NodePort{
				{Name: "brief", Type: "text"},
				{Name: "input", Type: "text"},
				{Name: "html", Type: "text"},
				{Name: "code", Type: "text"},
				{Name: "images", Type: "image", Multiple: true},
				{Name: "references", Type: "image", Multiple: true},
				{Name: "critique", Type: "critique"},
				{Name: "feedback", Type: "text"},
			},
			Outputs: []NodePort{
				{Name: "html", Type: "text"},
				{Name: "artifact", Type: "text"},
				{Name: "critique", Type: "critique"},
				{Name: "output", Type: "json"},
				{Name: "candidates", Type: "candidates"},
			},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":         map[string]any{"type": "string", "title": "Label"},
					"agent_id":      map[string]any{"type": "string", "title": "Agent"},
					"agent_version": map[string]any{"type": "number", "title": "Agent version (optional pin)"},
					"model_set_id":  map[string]any{"type": "string", "title": "Model set override"},
					"count": map[string]any{"type": "number", "title": "Candidates (artifact agents; >1 runs them in parallel → candidates port)",
						"default": 1, "minimum": 1, "maximum": MaxCandidates},
					"variations": map[string]any{"type": "array", "title": "Per-candidate direction hints", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"agent_id"},
			},
			Defaults: map[string]any{"label": "Agent"},
		},
		{
			Type:        "pick_best",
			Name:        "Pick best",
			Description: "Renders every candidate and lets a vision judge agent pick the best; the winner continues as html.",
			Inputs: []NodePort{
				{Name: "candidates", Type: "candidates"},
				{Name: "brief", Type: "text"},
			},
			Outputs: []NodePort{
				{Name: "html", Type: "text"},
				{Name: "ranking", Type: "json"},
			},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":          map[string]any{"type": "string", "title": "Label"},
					"judge_agent_id": map[string]any{"type": "string", "title": "Judge agent (default: vision-judge)"},
					"criteria":       map[string]any{"type": "string", "title": "Extra judging criteria"},
					"width":          map[string]any{"type": "number", "title": "Render width (px)", "default": 1280},
					"color_scheme":   map[string]any{"type": "string", "title": "Color scheme", "enum": []string{"light", "dark"}, "default": "light"},
				},
			},
			Defaults: map[string]any{"label": "Pick best", "width": 1280, "color_scheme": "light"},
		},
		{
			Type:        "validate",
			Name:        "Validate",
			Description: "Runs the component validator on an HTML fragment.",
			Inputs:      []NodePort{{Name: "html", Type: "text"}},
			Outputs:     []NodePort{{Name: "report", Type: "json"}},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":       map[string]any{"type": "string", "title": "Label"},
					"rule_set_id": map[string]any{"type": "string", "title": "Rule set (optional)"},
				},
			},
			Defaults: map[string]any{"label": "Validate"},
		},
		{
			Type:        "render",
			Name:        "Render",
			Description: "Screenshots an HTML fragment and reports layout metrics.",
			Inputs:      []NodePort{{Name: "html", Type: "text"}},
			Outputs: []NodePort{
				{Name: "images", Type: "image", Multiple: true},
				{Name: "metrics", Type: "json"},
			},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":        map[string]any{"type": "string", "title": "Label"},
					"width":        map[string]any{"type": "number", "title": "Width (px)", "default": 1280},
					"color_scheme": map[string]any{"type": "string", "title": "Color scheme", "enum": []string{"light", "dark"}, "default": "light"},
					"dpr":          map[string]any{"type": "number", "title": "Device scale factor", "default": 2},
					"variants": map[string]any{
						"type": "array", "title": "Variants",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"width":        map[string]any{"type": "number", "title": "Width (px)"},
								"color_scheme": map[string]any{"type": "string", "title": "Color scheme", "enum": []string{"light", "dark"}},
							},
						},
					},
				},
			},
			Defaults: map[string]any{"label": "Render", "width": 1280, "dpr": 2, "color_scheme": "light"},
		},
		{
			Type:        "merge",
			Name:        "Merge",
			Description: "Merges several critiques into one.",
			Inputs:      []NodePort{{Name: "critique", Type: "critique", Multiple: true}},
			Outputs:     []NodePort{{Name: "critique", Type: "critique"}},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":  map[string]any{"type": "string", "title": "Label"},
					"dedupe": map[string]any{"type": "boolean", "title": "Dedupe issues", "default": true},
				},
			},
			Defaults: map[string]any{"label": "Merge", "dedupe": true},
		},
		{
			Type:        "gate",
			Name:        "Gate",
			Description: "Loops on critique: continue for another iteration or finish when good enough.",
			Inputs:      []NodePort{{Name: "critique", Type: "critique"}},
			Outputs: []NodePort{
				{Name: "continue", Type: "text"},
				{Name: "done", Type: "text"},
			},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":          map[string]any{"type": "string", "title": "Label"},
					"min_score":      map[string]any{"type": "number", "title": "Minimum score (0-10)"},
					"max_iterations": map[string]any{"type": "number", "title": "Max iterations", "default": 3},
					"patience":       map[string]any{"type": "number", "title": "No-improvement patience", "default": 1},
					"budget_usd":     map[string]any{"type": "number", "title": "Budget (USD, optional)"},
					"expr":           map[string]any{"type": "string", "title": "Advanced expression (optional)"},
				},
			},
			Defaults: map[string]any{"label": "Gate", "max_iterations": 3, "patience": 1},
		},
		{
			Type:        "output",
			Name:        "Output",
			Description: "Collects the run result.",
			Inputs: []NodePort{
				{Name: "html", Type: "text"},
				{Name: "critique", Type: "critique"},
			},
			Outputs: []NodePort{},
			ConfigSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label":    map[string]any{"type": "string", "title": "Label"},
					"strategy": map[string]any{"type": "string", "title": "Strategy", "enum": []string{"best", "last"}, "default": "best"},
				},
			},
			Defaults: map[string]any{"label": "Output", "strategy": "best"},
		},
	}
}

// CatalogTools is the tool list the runtime wires (Workstream E): the flags
// name the capability group ("fs", "sandbox", "validate", "terminal").
var CatalogTools = []ToolInfo{
	{Name: "read_file", Description: "Read a file from the session workspace or mounted skills/rules.", Flags: []string{"fs"}},
	{Name: "write_file", Description: "Create or overwrite a file in the session workspace (/work).", Flags: []string{"fs"}},
	{Name: "edit_file", Description: "Apply an exact text replacement inside a workspace file.", Flags: []string{"fs"}},
	{Name: "glob", Description: "List files matching a glob pattern.", Flags: []string{"fs"}},
	{Name: "grep", Description: "Search file contents with a regular expression.", Flags: []string{"fs"}},
	{Name: "run_js", Description: "Run JavaScript in the sandboxed (no fs, no net) runtime.", Flags: []string{"sandbox"}},
	{Name: "validate_component", Description: "Validate an HTML component fragment and return the violation report.", Flags: []string{"validate"}},
	{Name: "generate_components", Description: "Generate several candidate components in parallel (worker generator agent); files land in /work/candidates/.", Flags: []string{"candidates"}},
	{Name: "pick_best", Description: "Render candidates and let a vision judge agent rank them and pick the best.", Flags: []string{"candidates"}},
	{Name: "critique_component", Description: "Render one component and get a structured critique from a critic agent.", Flags: []string{"candidates"}},
	{Name: "submit_artifact", Description: "Submit the final HTML component (or several, via paths) and finish. Terminal tool.", Flags: []string{"terminal"}},
	{Name: "submit_critique", Description: "Submit the structured critique and finish. Terminal tool.", Flags: []string{"terminal"}},
	{Name: "submit_fields", Description: "Submit the custom-contract fields and finish. Terminal tool.", Flags: []string{"terminal"}},
}

// IsCatalogTool reports whether name is a catalog tool.
func IsCatalogTool(name string) bool {
	for _, t := range CatalogTools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// catalogPromptSections mirrors plan §4.3.
func catalogPromptSections() []PromptSection {
	return []PromptSection{
		{Slug: "rules", Title: "Rules", Description: "Attached rule groups, rendered as ## Rules."},
		{Slug: "skills", Title: "Skills", Description: "Attached skills, mounted read-only under /skills/<slug>/."},
		{Slug: "output", Title: "Output", Description: "The output contract: how the agent must submit its result."},
	}
}

// catalogPromptVariables lists the placeholders usable in prompts and templates.
func catalogPromptVariables() []PromptVariable {
	return []PromptVariable{
		{Name: "brief", Description: "The run brief from the Input node."},
		{Name: "input", Description: "Free-form text input wired to the agent."},
		{Name: "html", Description: "The current HTML fragment."},
		{Name: "code", Description: "Code input (when wired)."},
		{Name: "images", Description: "Image inputs (screenshots, references)."},
		{Name: "references", Description: "Reference images from the Input node."},
		{Name: "critique", Description: "The previous critique."},
		{Name: "feedback", Description: "The gate's feedback from the previous iteration (iteration 1: the chat's canvas context)."},
		{Name: "canvas", Description: "The chat's canvas: file tree, the user's recent changes, selected components and where the result goes."},
		{Name: "output", Description: "Custom output-contract fields (agents with a custom contract)."},
		{Name: "criteria", Description: "Judging criteria (pick_best judges)."},
		{Name: "variation", Description: "The direction hint of one candidate (generate_components / Agent node count)."},
	}
}

/* ---------- building the catalog ---------- */

// BuildCatalogData assembles the live catalog from the store and the llm
// service. Exported for callers outside the package (the agentrun validate
// route builds a catalog to validate a pipeline body).
func BuildCatalogData(ctx context.Context, store *Store, models *llm.Service) (*CatalogData, error) {
	modelSets, err := studioRefs(ctx, store, KindModelSet)
	if err != nil {
		return nil, err
	}
	skills, err := studioRefs(ctx, store, KindSkill)
	if err != nil {
		return nil, err
	}
	rules, err := studioRefs(ctx, store, KindRule)
	if err != nil {
		return nil, err
	}
	agents, err := studioRefs(ctx, store, KindAgent)
	if err != nil {
		return nil, err
	}

	modelsOut := make([]CatalogModel, 0, 16)
	for _, m := range models.EnabledModels() {
		modelsOut = append(modelsOut, CatalogModel{
			ProviderID:  m.ProviderID,
			ModelID:     m.ModelID,
			DisplayName: m.DisplayName,
			Capabilities: CatalogModelCaps{
				Vision:           m.Vision,
				Tools:            m.Tools,
				ReasoningOptions: m.ReasoningOptions,
				ContextWindow:    m.ContextWindow,
				MaxOutput:        m.MaxOutput,
			},
			Enabled: true,
		})
	}

	return &CatalogData{
		PortTypes:          catalogPortTypes(),
		NodeTypes:          catalogNodeTypes(),
		Tools:              CatalogTools,
		Models:             modelsOut,
		ModelSets:          modelSets,
		Skills:             skills,
		Rules:              rules,
		Agents:             agents,
		PromptSections:     catalogPromptSections(),
		PromptVariables:    catalogPromptVariables(),
		ValidatorRuleCodes: component.Codes(),
	}, nil
}

// BuildCatalog assembles the live catalog from the controller's dependencies.
func (c *Controller) BuildCatalog(ctx context.Context) (*CatalogData, error) {
	return BuildCatalogData(ctx, c.store, c.llm)
}

// studioRefs lists all defs of a kind (drafts included) as picker refs.
func studioRefs(ctx context.Context, store *Store, kind string) ([]StudioRef, error) {
	defs, err := store.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]StudioRef, len(defs))
	for i, d := range defs {
		out[i] = StudioRef{ID: d.ID, Slug: d.Slug, Name: d.Name}
	}
	return out, nil
}
