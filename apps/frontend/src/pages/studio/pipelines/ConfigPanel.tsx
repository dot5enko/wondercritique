/**
 * Right-hand config panel of the pipeline editor. With a node selected:
 * type badge + read-only node id + delete, common Label, per-type extras
 * (agent picker / version pin / model-set override for Agent nodes, the
 * form-fields editor for Input nodes) and the JSON-schema config form. With
 * a wire selected: endpoints, loop badge and delete.
 *
 * Keys managed by the extras (label, agent_id/agent_version/model_set_id,
 * fields) are filtered out of the generated SchemaForm so each value has
 * exactly one editor. If the node's config_schema declares those properties
 * the same names are used; otherwise the conventional names are stored
 * alongside the schema config inside `node.config`.
 */
import { ArrowRight, MousePointerClick, Plus, Trash2, X } from 'lucide-react'
import { Badge, Button, Callout, EmptyState, Field, IconButton, Input, Select, cx, type SelectOption } from '../../../ui'

import { SchemaForm } from '../../../components'
import type { JSONSchema, NodeType } from '../../../api/studio'
import {
  isObj,
  nodeTitle,
  nodeTypeOf,
  portTypeLabel,
  type EditorRefs,
  type PipelineRFEdge,
  type PipelineRFNode,
} from './flow'

export type PanelTarget =
  | { kind: 'node'; node: PipelineRFNode }
  | { kind: 'edge'; edge: PipelineRFEdge }
  | null

export interface ConfigPanelProps {
  target: PanelTarget
  nodes: PipelineRFNode[]
  edges: PipelineRFEdge[]
  refs: EditorRefs
  onNodeConfig: (id: string, config: Record<string, unknown>) => void
  /** Node deletion goes through the editor's ConfirmDialog. */
  onRequestNodeDelete: (id: string) => void
  onDeleteEdge: (id: string) => void
}

export function ConfigPanel({ target, nodes, edges, refs, onNodeConfig, onRequestNodeDelete, onDeleteEdge }: ConfigPanelProps) {
  if (!target) {
    return <EmptyState icon={MousePointerClick} title="Nothing selected" description="Select a node or a wire on the canvas to edit it." className="py-10" />
  }
  if (target.kind === 'node') {
    return (
      <NodePanel
        node={target.node}
        nodes={nodes}
        edges={edges}
        refs={refs}
        onConfig={onNodeConfig}
        onDeleteRequest={onRequestNodeDelete}
      />
    )
  }
  return <EdgePanel edge={target.edge} nodes={nodes} refs={refs} onDelete={onDeleteEdge} />
}

/* ---------- node panel ---------- */

function NodePanel({
  node,
  nodes,
  edges,
  refs,
  onConfig,
  onDeleteRequest,
}: {
  node: PipelineRFNode
  nodes: PipelineRFNode[]
  edges: PipelineRFEdge[]
  refs: EditorRefs
  onConfig: (id: string, config: Record<string, unknown>) => void
  onDeleteRequest: (id: string) => void
}) {
  const nt = nodeTypeOf(refs.catalog, node.data.nodeType)
  const config = node.data.config
  const patch = (next: Record<string, unknown>) => onConfig(node.id, next)
  const ctx = nodeContext(node, nodes, edges, refs)

  return (
    <div className="flex flex-col gap-4 p-3.5">
      <div className="flex items-center gap-2">
        <Badge tone="neutral">{nt?.name ?? node.data.nodeType}</Badge>
        <span className="min-w-0 flex-1 truncate font-mono text-2xs text-ink-3" title={node.id}>
          {node.id}
        </span>
        <IconButton
          icon={<Trash2 />}
          label="Delete node"
          size="sm"
          className="text-danger hover:bg-danger-soft"
          onClick={() => onDeleteRequest(node.id)}
        />
      </div>

      <div className="flex flex-col gap-1 rounded-lg border border-line bg-app/40 p-2.5 text-xs leading-relaxed">
        <p>
          <span className="font-medium text-ink-2">When:</span> <span className="text-ink-2">{ctx.when}</span>
        </p>
        <p>
          <span className="font-medium text-ink-2">Gets:</span>{' '}
          {ctx.gets.length > 0 ? (
            <span className="text-ink-2">{ctx.gets.join('; ')}</span>
          ) : (
            <span className="text-warning">nothing wired in yet</span>
          )}
        </p>
        <p>
          <span className="font-medium text-ink-2">Sends:</span>{' '}
          {ctx.sends.length > 0 ? (
            <span className="text-ink-2">{ctx.sends.join('; ')}</span>
          ) : (
            <span className="text-warning">nowhere yet</span>
          )}
        </p>
      </div>

      {node.data.nodeType === 'gate' && (
        <Callout tone="info" title="What the gate does">
          <p className="mt-1 text-xs">
            After each iteration the gate looks at the critique score: <b>continue</b> sends the work
            back for another round (wire it to the generator's feedback port), <b>done</b> finishes
            the run and the best-scoring iteration is kept. Configure the score threshold and the
            iteration cap below.
          </p>
        </Callout>
      )}

      {nt?.description && <p className="text-xs leading-relaxed text-ink-2">{nt.description}</p>}

      <Field label="Label" hint="Display name on the canvas — defaults to the node type.">
        <Input
          value={typeof config['label'] === 'string' ? (config['label'] as string) : ''}
          placeholder={nt?.name ?? node.data.nodeType}
          onChange={(e) => {
            const next = { ...config }
            if (e.target.value.trim() !== '') next['label'] = e.target.value
            else delete next['label']
            patch(next)
          }}
        />
      </Field>

      {node.data.nodeType === 'agent' && <AgentExtras config={config} refs={refs} schema={nt?.config_schema} onChange={patch} />}
      {node.data.nodeType === 'input' && <InputFieldsEditor config={config} onChange={patch} />}

      {nt ? (
        <section className="flex flex-col gap-1.5">
          <p className="text-xs font-medium text-ink-2">Configuration</p>
          <SchemaForm
            schema={schemaWithout(nt.config_schema, managedKeys(node.data.nodeType, nt))}
            value={config}
            onChange={(v) => patch(isObj(v) ? v : {})}
            idPrefix={`node-${node.id}`}
          />
        </section>
      ) : (
        <Callout tone="warning" title="Unknown node type">
          This node type is not in the catalog. Re-load the page or delete the node.
        </Callout>
      )}
    </div>
  )
}

/** Config keys the panel edits with dedicated controls, hidden from SchemaForm. */
function managedKeys(nodeType: string, nt: NodeType): string[] {
  const keys = ['label']
  if (nodeType === 'agent') {
    keys.push(schemaKeyOf(nt.config_schema, ['agent_id', 'agent', 'agent_slug'], 'agent_id'))
    keys.push(schemaKeyOf(nt.config_schema, ['agent_version', 'agent_version_pin', 'version_pin', 'version'], 'agent_version'))
    keys.push(schemaKeyOf(nt.config_schema, ['model_set_id', 'model_set'], 'model_set_id'))
  }
  if (nodeType === 'input') keys.push('fields')
  return keys
}

function AgentExtras({
  config,
  refs,
  schema,
  onChange,
}: {
  config: Record<string, unknown>
  refs: EditorRefs
  schema?: JSONSchema
  onChange: (next: Record<string, unknown>) => void
}) {
  const agentKey = schemaKeyOf(schema, ['agent_id', 'agent', 'agent_slug'], 'agent_id')
  const versionKey = schemaKeyOf(schema, ['agent_version', 'agent_version_pin', 'version_pin', 'version'], 'agent_version')
  const modelSetKey = schemaKeyOf(schema, ['model_set_id', 'model_set'], 'model_set_id')

  const agentId = typeof config[agentKey] === 'string' ? (config[agentKey] as string) : null
  const agent = refs.agents.find((a) => a.id === agentId) ?? null
  const versionRaw = config[versionKey]
  const versionValue = versionRaw == null ? 'latest' : String(versionRaw)
  const modelSetValue = typeof config[modelSetKey] === 'string' ? (config[modelSetKey] as string) : ''

  const write = (key: string, value: unknown) => {
    const next = { ...config }
    if (value === '' || value == null) delete next[key]
    else next[key] = coerceValue(schema, key, value)
    onChange(next)
  }

  const versionOptions: SelectOption[] = [
    { value: 'latest', label: 'Latest published' },
    { value: 'draft', label: 'Follow draft' },
  ]
  if (agent?.latest_version != null) {
    for (let v = agent.latest_version; v >= 1; v--) versionOptions.push({ value: String(v), label: `Pinned v${v}` })
  }
  if (!versionOptions.some((o) => o.value === versionValue)) {
    versionOptions.push({ value: versionValue, label: `Current: ${versionValue}` })
  }

  return (
    <section className="flex flex-col gap-3 rounded-lg border border-line bg-app/40 p-3">
      <p className="text-xs font-medium text-ink-2">Agent</p>
      <Field label="Agent">
        <Select
          size="sm"
          options={refs.agents.map((a) => ({ value: a.id, label: a.name, hint: a.slug }))}
          value={agentId}
          placeholder={refs.agents.length === 0 ? 'No agents defined yet' : 'Select an agent…'}
          disabled={refs.agents.length === 0}
          onChange={(v) => onChange({ ...config, [agentKey]: v, [versionKey]: 'latest' })}
          ariaLabel="Agent"
        />
      </Field>
      <Field label="Version pin" hint="Which published version of the agent runs.">
        <Select size="sm" options={versionOptions} value={versionValue} disabled={!agent} onChange={(v) => write(versionKey, v)} ariaLabel="Version pin" />
      </Field>
      <Field label="Model set" optional hint="Empty inherits the agent's own set.">
        <Select
          size="sm"
          options={[{ value: '', label: 'Inherit agent default' }, ...(refs.catalog?.model_sets ?? []).map((ms) => ({ value: ms.id, label: ms.name, hint: ms.slug }))]}
          value={modelSetValue}
          onChange={(v) => write(modelSetKey, v)}
          ariaLabel="Model set override"
        />
      </Field>
    </section>
  )
}

const FIELD_TYPES: SelectOption[] = [
  { value: 'text', label: 'Text' },
  { value: 'textarea', label: 'Text area' },
  { value: 'image', label: 'Image' },
]

/** Rows of {key, label, type} producing the Input node's `config.fields`. */
function InputFieldsEditor({ config, onChange }: { config: Record<string, unknown>; onChange: (next: Record<string, unknown>) => void }) {
  const rawFields = Array.isArray(config['fields']) ? (config['fields'] as unknown[]) : []

  const writeFields = (next: unknown[]) => onChange({ ...config, fields: next })
  const updateAt = (i: number, patch: Record<string, unknown>) => {
    const next = [...rawFields]
    next[i] = { ...(isObj(next[i]) ? next[i] : {}), ...patch }
    writeFields(next)
  }
  const removeAt = (i: number) => writeFields(rawFields.filter((_, k) => k !== i))
  const add = () => writeFields([...rawFields, { key: '', label: '', type: 'text' }])

  return (
    <section className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between">
        <p className="text-xs font-medium text-ink-2">Form fields</p>
        <Button size="sm" variant="ghost" icon={<Plus />} onClick={add}>
          Add field
        </Button>
      </div>
      {rawFields.length === 0 && <p className="text-xs text-ink-3">No fields yet — the run form will start empty.</p>}
      <div className="flex flex-col gap-1.5">
        {rawFields.map((f, i) => {
          const o = isObj(f) ? f : {}
          const key = typeof o.key === 'string' ? o.key : ''
          const label = typeof o.label === 'string' ? o.label : ''
          const type = typeof o.type === 'string' ? o.type : 'text'
          return (
            <div key={i} className="flex items-center gap-1.5">
              <Input
                size="sm"
                variant="filled"
                placeholder="key"
                aria-label={`Field ${i + 1} key`}
                className="font-mono text-xs"
                value={key}
                onChange={(e) => updateAt(i, { key: e.target.value })}
              />
              <Input
                size="sm"
                variant="filled"
                placeholder="Label"
                aria-label={`Field ${i + 1} label`}
                value={label}
                onChange={(e) => updateAt(i, { label: e.target.value })}
              />
              <Select
                size="sm"
                options={FIELD_TYPES}
                value={FIELD_TYPES.some((t) => t.value === type) ? type : 'text'}
                onChange={(v) => updateAt(i, { type: v })}
                className="w-24 shrink-0"
                ariaLabel={`Field ${i + 1} type`}
              />
              <IconButton icon={<X />} label={`Remove field ${i + 1}`} size="sm" onClick={() => removeAt(i)} />
            </div>
          )
        })}
      </div>
    </section>
  )
}

/* ---------- node context (When / Gets / Sends) ---------- */

/** Human summary of where a node sits in the graph, from the current wires. */
function nodeContext(
  node: PipelineRFNode,
  nodes: PipelineRFNode[],
  edges: PipelineRFEdge[],
  refs: EditorRefs,
): { when: string; gets: string[]; sends: string[] } {
  const label = (id: string): string => {
    const n = nodes.find((x) => x.id === id)
    return n ? nodeTitle(n.data, refs) : id
  }
  const incoming = edges.filter((e) => e.target === node.id)
  const outgoing = edges.filter((e) => e.source === node.id)
  const sources = [...new Set(incoming.map((e) => label(e.source)))]
  const looped = incoming.some((e) => e.data?.loop)

  let when: string
  if (node.data.nodeType === 'input') when = 'At the start of every iteration — it feeds the run’s input'
  else if (looped) when = 'When the Gate says continue — the next iteration starts here'
  else if (sources.length > 1) when = `After ${sources.join(' and ')} finish (they run in parallel)`
  else if (sources.length === 1) when = `After ${sources[0]} finishes`
  else when = 'Once its input is wired and everything upstream is ready'

  const gets = incoming.map((e) => `${e.targetHandle ?? '?'} ← ${label(e.source)}`)
  const sends = outgoing.map((e) => `${e.sourceHandle ?? '?'} → ${label(e.target)}`)
  return { when, gets, sends }
}

/* ---------- edge panel ---------- */

function EdgePanel({
  edge,
  nodes,
  refs,
  onDelete,
}: {
  edge: PipelineRFEdge
  nodes: PipelineRFNode[]
  refs: EditorRefs
  onDelete: (id: string) => void
}) {
  const src = nodes.find((n) => n.id === edge.source)
  const dst = nodes.find((n) => n.id === edge.target)
  const loop = edge.data?.loop === true
  return (
    <div className="flex flex-col gap-4 p-3.5">
      <div className="flex items-center gap-2">
        <Badge tone="neutral">Wire</Badge>
        {loop && (
          <Badge tone="accent" dot>
            Loop
          </Badge>
        )}
        <span className="flex-1" />
        <IconButton icon={<Trash2 />} label="Delete wire" size="sm" className="text-danger hover:bg-danger-soft" onClick={() => onDelete(edge.id)} />
      </div>

      <div className="flex flex-col gap-2 rounded-lg border border-line bg-app/40 p-3 text-xs">
        <Endpoint label="From" node={src} port={edge.sourceHandle} dir="from" refs={refs} />
        <div className="flex justify-center text-ink-3">
          <ArrowRight size={13} />
        </div>
        <Endpoint label="To" node={dst} port={edge.targetHandle} dir="to" refs={refs} />
      </div>

      <p className={cx('text-2xs text-ink-3')}>
        Wire id: <span className="font-mono">{edge.id}</span>
      </p>
    </div>
  )
}

function Endpoint({
  label,
  node,
  port,
  dir,
  refs,
}: {
  label: string
  node?: PipelineRFNode
  port?: string | null
  dir: 'from' | 'to'
  refs: EditorRefs
}) {
  const nt = node ? nodeTypeOf(refs.catalog, node.data.nodeType) : undefined
  const portType = port && nt ? (dir === 'from' ? nt.outputs : nt.inputs).find((p) => p.name === port)?.type ?? null : null
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="w-8 shrink-0 text-2xs text-ink-3">{label}</span>
      <span className="min-w-0 truncate text-ink">{node ? nodeTitle(node.data, refs) : 'Unknown node'}</span>
      <span className="font-mono text-2xs text-ink-3" title={portType ? portTypeLabel(refs.catalog, portType) : undefined}>
        {port ?? '—'}
      </span>
    </div>
  )
}

/* ---------- schema helpers ---------- */

/** First declared property among `candidates`, else the fallback name. */
function schemaKeyOf(schema: JSONSchema | undefined, candidates: string[], fallback: string): string {
  const props = schema?.properties
  if (props) for (const c of candidates) if (c in props) return c
  return fallback
}

/** Drops managed properties so SchemaForm doesn't render duplicate editors. */
function schemaWithout(schema: JSONSchema | undefined, keys: string[]): JSONSchema | undefined {
  const props = schema?.properties
  if (!props) return schema
  const next = { ...props }
  let changed = false
  for (const k of keys) {
    if (k in next) {
      delete next[k]
      changed = true
    }
  }
  return changed ? { ...schema, properties: next } : schema
}

/** Pure-digit version pins become numbers unless the schema says string. */
function coerceValue(schema: JSONSchema | undefined, key: string, value: unknown): unknown {
  if (typeof value === 'string' && /^\d+$/.test(value)) {
    const prop = schema?.properties?.[key]
    const t = prop && typeof prop.type === 'string' ? prop.type : undefined
    if (!prop || t === 'integer' || t === 'number') return Number(value)
  }
  return value
}
