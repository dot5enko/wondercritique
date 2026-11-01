/**
 * Studio API — admin-only definition CRUD, catalog, and validation
 * (plan.critique-engine.md §4.2, §4.7).
 *
 * All paths are relative to `/api/v1`, so `/studio/catalog` below is really
 * `GET /api/v1/studio/catalog`.
 *
 * Where the plan leaves the exact wire shape open the choice is documented
 * with an "ASSUMPTION" comment — adjust those when the backend lands.
 */
import { apiGet, apiPost, apiPut, toQuery } from './client'
import { mapAgentRunEvent, type AgentRunEvent } from './agents'
import { postSSETyped } from './sse'

/* ---------- primitives ---------- */

/** Minimal JSON Schema subset for config/fields schemas (extended freely by the backend). */
export interface JSONSchema {
  type?: string
  title?: string
  description?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  items?: JSONSchema | JSONSchema[]
  enum?: unknown[]
  default?: unknown
  [key: string]: unknown
}

export type StudioKind = 'model_set' | 'agent' | 'pipeline' | 'skill' | 'rule'

/** Reference to another definition, used by catalog pickers. */
export interface StudioRef {
  id: string
  slug: string
  name: string
}

/* ---------- catalog ---------- */

/** A named port on a node type. `type` references a {@link PortType} id. */
export interface NodePort {
  name: string
  type: string
  /** ASSUMPTION: an input may accept several wires of this type (e.g. Merge inputs). */
  multiple?: boolean
}

/** ASSUMPTION: port types are id + human label (e.g. `text`, `html`, `image`, `critique`). */
export interface PortType {
  id: string
  label: string
}

export interface NodeType {
  /** e.g. `input`, `agent`, `validate`, `render`, `merge`, `gate`, `output`. */
  type: string
  name: string
  description: string
  inputs: NodePort[]
  outputs: NodePort[]
  /** JSON Schema describing the node's `config` object (forms are generated from it). */
  config_schema: JSONSchema
  defaults?: Record<string, unknown>
}

/** Tools available to agents (per-agent allowlist in the agent editor). */
export interface ToolInfo {
  name: string
  description: string
  /** Capability flags from the backend, e.g. `terminal`, `sandbox`, `render`. */
  flags: string[]
}

/** Capabilities of one model as parsed from models.dev (plan §3.1). */
export interface ModelCapabilities {
  /** `modalities.input` contains `image`. */
  vision: boolean
  /** `tool_call` supported. */
  tools: boolean
  /** Allowed reasoning-effort values, e.g. `["low","medium","high"]`; empty when not a reasoning model. */
  reasoning_options: string[]
  context_window: number
  max_output: number
}

/** One model from an enabled provider. */
export interface CatalogModel {
  provider_id: string
  model_id: string
  display_name: string
  capabilities: ModelCapabilities
  enabled: boolean
}

/** Auto-appended prompt sections (plan §4.3): `## Rules`, `## Skills`, `## Output`. */
export interface PromptSection {
  slug: string
  title: string
  description?: string
}

/** Placeholders usable in prompts/input templates, e.g. `{{port_name}}`. */
export interface PromptVariable {
  name: string
  description?: string
}

export interface StudioCatalog {
  node_types: NodeType[]
  port_types: PortType[]
  tools: ToolInfo[]
  /** Models from enabled providers only — pickable in model sets / agents / Agent nodes. */
  models: CatalogModel[]
  model_sets: StudioRef[]
  skills: StudioRef[]
  rules: StudioRef[]
  prompt_sections: PromptSection[]
  prompt_variables: PromptVariable[]
  /** Rule codes known to the component validator, for the rules editor dropdown. */
  validator_rule_codes: string[]
}

export function getCatalog(signal?: AbortSignal): Promise<StudioCatalog> {
  return apiGet<StudioCatalog>('/studio/catalog', { signal })
}

/* ---------- definition bodies (typed per kind) ---------- */

export interface ModelParams {
  /** One of the model's `reasoning_options` (catalog), e.g. `low` | `medium` | `high`. */
  reasoning_effort?: string
  max_output_tokens?: number
  [key: string]: unknown
}

export interface ModelSetEntry {
  provider_id: string
  model_id: string
  params?: ModelParams
}

export interface ModelSetBody {
  strategy: 'single' | 'fallback' | 'fanout'
  entries: ModelSetEntry[]
}

export interface AgentOutputContract {
  type: 'artifact' | 'critique' | 'custom'
  /** Required when type is `custom` (JSON Schema of the structured fields). */
  fields_schema?: JSONSchema
}

export interface AgentLimits {
  max_steps?: number
  max_iterations?: number
}

export interface AgentBody {
  model_set_id: string
  system_prompt: string
  input_template: string
  tools: string[]
  skill_ids: string[]
  rule_ids: string[]
  output_contract: AgentOutputContract
  limits: AgentLimits
}

export interface PipelineNode {
  id: string
  /** One of catalog `node_types[].type`. */
  type: string
  config: Record<string, unknown>
  position: { x: number; y: number }
}

export interface PipelineEdge {
  id: string
  from: { node: string; port: string }
  to: { node: string; port: string }
  /** `true` for the Gate `continue` loop edge (plan §4.5); everything else must form a DAG. */
  loop?: boolean
}

export interface PipelineBody {
  nodes: PipelineNode[]
  edges: PipelineEdge[]
}

export interface SkillFile {
  /** Path relative to the skill root, e.g. `SKILL.md`, `refs/foo.png`, `lib/util.js`. */
  path: string
  content: string
}

export interface SkillBody {
  files: SkillFile[]
}

export interface RuleItem {
  text: string
  severity: string
  /** Optional component-validator rule code (see catalog `validator_rule_codes`). */
  validator_code?: string
}

export interface RuleGroup {
  name: string
  rules: RuleItem[]
}

export interface RuleBody {
  groups: RuleGroup[]
}

/** Body struct per kind — used to type `draft`/`body` fields by definition kind. */
export interface BodyOf {
  model_set: ModelSetBody
  agent: AgentBody
  pipeline: PipelineBody
  skill: SkillBody
  rule: RuleBody
}

export type StudioBody = BodyOf[StudioKind]

/* ---------- definition records & versions ---------- */

export interface StudioDef<K extends StudioKind = StudioKind> {
  id: string
  kind: K
  slug: string
  name: string
  description: string
  draft: BodyOf[K] | null
  draft_updated_at: string | null
  latest_version: number | null
  archived: boolean
}

/** An immutable published version (plan §4.1 `studio_def_versions`). */
export interface StudioVersion<K extends StudioKind = StudioKind> {
  version: number
  note: string
  published_by: string
  published_at: string
  body: BodyOf[K]
}

/* ---------- generic definition CRUD ---------- */

/**
 * ASSUMPTION: the kind string is used verbatim as the path segment
 * (`/studio/model_set`, `/studio/agent`, …) — matching `/{kind}` in plan §4.7.
 */
function kindPath(kind: StudioKind, ...rest: string[]): string {
  return `/studio/${kind}${rest.map((r) => `/${r}`).join('')}`
}

export function listDefs<K extends StudioKind>(
  kind: K,
  params?: Record<string, string | number | boolean | undefined | null>,
  signal?: AbortSignal,
): Promise<{ items: StudioDef<K>[]; next_cursor?: string | null }> {
  return apiGet<{ items: StudioDef<K>[]; next_cursor?: string | null }>(kindPath(kind) + toQuery(params ?? {}), { signal })
}

export function getDef<K extends StudioKind>(kind: K, id: string, signal?: AbortSignal): Promise<StudioDef<K>> {
  return apiGet<StudioDef<K>>(kindPath(kind, id), { signal })
}

/** ASSUMPTION: creation accepts identity + initial draft in one body. */
export interface CreateDefBody<K extends StudioKind> {
  slug: string
  name: string
  description?: string
  draft: BodyOf[K]
}

export function createDef<K extends StudioKind>(kind: K, body: CreateDefBody<K>, signal?: AbortSignal): Promise<StudioDef<K>> {
  return apiPost<StudioDef<K>>(kindPath(kind), body, { signal })
}

/** Autosaves the draft. ASSUMPTION: the backend answers with the refreshed def. */
export function putDraft<K extends StudioKind>(kind: K, id: string, draft: BodyOf[K], signal?: AbortSignal): Promise<StudioDef<K>> {
  return apiPut<StudioDef<K>>(kindPath(kind, id, 'draft'), draft, { signal })
}

/** Publishes the current draft as a new immutable version. */
export function publishDef<K extends StudioKind>(kind: K, id: string, note: string, signal?: AbortSignal): Promise<StudioVersion<K>> {
  return apiPost<StudioVersion<K>>(kindPath(kind, id, 'publish'), { note }, { signal })
}

export function listVersions<K extends StudioKind>(
  kind: K,
  id: string,
  signal?: AbortSignal,
): Promise<{ items: StudioVersion<K>[] }> {
  return apiGet<{ items: StudioVersion<K>[] }>(kindPath(kind, id, 'versions'), { signal })
}

export function getVersion<K extends StudioKind>(kind: K, id: string, version: number, signal?: AbortSignal): Promise<StudioVersion<K>> {
  return apiGet<StudioVersion<K>>(kindPath(kind, id, 'versions', String(version)), { signal })
}

/** Copies a published version back into the draft. ASSUMPTION: answers with the refreshed def. */
export function restoreVersion<K extends StudioKind>(kind: K, id: string, version: number, signal?: AbortSignal): Promise<StudioDef<K>> {
  return apiPost<StudioDef<K>>(kindPath(kind, id, 'restore', String(version)), undefined, { signal })
}

/* ---------- agent preview & try ---------- */

/** Sample values for the agent's input ports (plan §4.3 "show assembled prompt"). */
export interface AgentPreviewRequest {
  input: Record<string, unknown>
}

/** The final assembled prompt texts. */
export interface AgentPreview {
  system: string
  input: string
}

export function previewAgent(id: string, sample: AgentPreviewRequest, signal?: AbortSignal): Promise<AgentPreview> {
  return apiPost<AgentPreview>(`/studio/agents/${id}/preview`, sample, { signal })
}

/**
 * Runs the agent once on sample input (draft allowed) and streams the same
 * run events as chat ({@link AgentRunEvent}); the terminal `done` frame is
 * mapped to null and skipped.
 */
export function tryAgent(
  id: string,
  sample: AgentPreviewRequest,
  opts: { signal?: AbortSignal; onEvent: (event: AgentRunEvent) => void },
): Promise<void> {
  return postSSETyped<AgentRunEvent>(`/studio/agents/${id}/try`, sample, {
    signal: opts.signal,
    map: mapAgentRunEvent,
    onEvent: opts.onEvent,
  })
}

/* ---------- pipeline validation ---------- */

export type ValidationSeverity = 'error' | 'warning'

export interface PipelineIssue {
  node_id: string
  message: string
  severity: ValidationSeverity
}

export interface PipelineValidation {
  ok: boolean
  issues: PipelineIssue[]
}

export function validatePipeline(id: string, signal?: AbortSignal): Promise<PipelineValidation> {
  return apiPost<PipelineValidation>(`/studio/pipelines/${id}/validate`, undefined, { signal })
}

/* ---------- export / import (JSON bundle, plan §4.1) ---------- */

/**
 * ASSUMPTION: the bundle format is backend-defined; the fields below are what
 * the plan implies (kind + slug + body per def). Kept permissive with an index
 * signature so unknown extras round-trip through import untouched.
 */
export interface StudioExportBundle {
  version?: number
  exported_at?: string
  defs?: Array<{
    kind: StudioKind
    slug: string
    name?: string
    description?: string
    body?: StudioBody
    /** Version to pin on import, when the bundle includes published versions. */
    version?: number
  }>
  [key: string]: unknown
}

export interface StudioImportResult {
  imported?: number
  skipped?: number
  errors?: Array<{ slug: string; error: string }>
  [key: string]: unknown
}

export function exportBundle(signal?: AbortSignal): Promise<StudioExportBundle> {
  return apiGet<StudioExportBundle>('/studio/export', { signal })
}

export function importBundle(bundle: StudioExportBundle, signal?: AbortSignal): Promise<StudioImportResult> {
  return apiPost<StudioImportResult>('/studio/import', bundle, { signal })
}
