/**
 * Agents studio — shared helpers (plan §4.4): the default agent body, slug
 * rules, sample-input seeds for preview/try runs, and the custom
 * output-contract field rows ⇄ JSON Schema compilation.
 */
import type { AgentBody, JSONSchema, StudioCatalog } from '../../../api/studio'

/** Slugs are lowercase alphanumerics + dashes (used as API path segments). */
export const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

/** Lowercases and dashes anything non-alphanumeric, e.g. "Code Critic v2" → `code-critic-v2`. */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64)
}

/** Fresh agent draft: no model set yet (placeholder), empty prompts, no tools. */
export function sensibleDefaultAgentBody(): AgentBody {
  return {
    model_set_id: '',
    system_prompt: '',
    input_template: '',
    tools: [],
    skill_ids: [],
    rule_ids: [],
    output_contract: { type: 'critique' },
    limits: {},
  }
}

/** Fills defaults and turns null lists (Go nil slices) into empty arrays. */
export function normalizeAgentBody(body: AgentBody | null | undefined): AgentBody {
  const base = sensibleDefaultAgentBody()
  if (!body) return base
  return {
    ...base,
    ...body,
    tools: body.tools ?? [],
    skill_ids: body.skill_ids ?? [],
    rule_ids: body.rule_ids ?? [],
  }
}

/**
 * Sample input seeding both "Show assembled prompt" and Try it: one
 * `'sample'` value per catalog prompt variable (plan §4.3/§4.4).
 */
export function defaultSampleInput(catalog: StudioCatalog | null | undefined): Record<string, string> {
  const vars = catalog?.prompt_variables ?? []
  return Object.fromEntries(vars.map((v) => [v.name, 'sample']))
}

/* ---------- custom output contract: field rows ⇄ JSON Schema ---------- */

export type FieldType = 'string' | 'number' | 'boolean' | 'enum'

export interface FieldRow {
  id: string
  name: string
  type: FieldType
  description: string
  required: boolean
  /** Comma-separated enum values (only meaningful when `type` is `enum`). */
  enumValues: string
}

let rowSeq = 0

function nextRowId(): string {
  return `field-${++rowSeq}`
}

/** A blank row for the custom output-contract editor. */
export function newFieldRow(): FieldRow {
  return { id: nextRowId(), name: '', type: 'string', description: '', required: false, enumValues: '' }
}

const SCALAR_TYPES: FieldType[] = ['string', 'number', 'boolean']

/** Reads the field-editor rows back out of a compiled `fields_schema`. */
export function parseFields(schema: JSONSchema | undefined | null): FieldRow[] {
  const properties = schema?.properties
  if (!properties || typeof properties !== 'object') return []
  const requiredRaw = schema && Array.isArray(schema.required) ? schema.required : []
  const required = requiredRaw.filter((r): r is string => typeof r === 'string')
  return Object.entries(properties).map(([name, raw]) => {
    const prop = (raw ?? {}) as JSONSchema
    const enumValues = Array.isArray(prop.enum) ? prop.enum : undefined
    const rawType = typeof prop.type === 'string' ? prop.type : 'string'
    return {
      id: nextRowId(),
      name,
      type: enumValues ? 'enum' : SCALAR_TYPES.includes(rawType as FieldType) ? (rawType as FieldType) : 'string',
      description: typeof prop.description === 'string' ? prop.description : '',
      required: required.includes(name),
      enumValues: enumValues ? enumValues.map((v) => String(v)).join(', ') : '',
    }
  })
}

/**
 * Compiles the rows into the `{type:'object', properties, required}` JSON
 * Schema sent as `output_contract.fields_schema`. Rows with a blank name are
 * skipped (they are still kept in the editor as work in progress).
 */
export function compileFields(rows: FieldRow[]): JSONSchema {
  const properties: Record<string, JSONSchema> = {}
  const required: string[] = []
  for (const row of rows) {
    const name = row.name.trim()
    if (!name) continue
    const prop: JSONSchema = { type: row.type === 'enum' ? 'string' : row.type }
    const description = row.description.trim()
    if (description) prop.description = description
    if (row.type === 'enum') {
      prop.enum = row.enumValues
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
    }
    properties[name] = prop
    if (row.required) required.push(name)
  }
  const schema: JSONSchema = { type: 'object', properties }
  if (required.length > 0) schema.required = required
  return schema
}
