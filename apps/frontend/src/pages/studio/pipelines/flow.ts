/**
 * Pipeline editor data layer — React Flow ↔ PipelineBody conversion, wire
 * rules (plan.critique-engine.md §4.5) and client-side lint.
 *
 * Mapping decisions:
 * - Every pipeline node renders as ONE generic React Flow node type
 *   (`wc-pipeline`); the actual pipeline node type rides in `data.nodeType`.
 *   RF's built-in `input`/`output`/`default` node types must stay free, and
 *   pipeline node types are exactly `input`/`output`/…
 * - The node display label lives at `config.label` — the PipelineBody
 *   contract has no dedicated label field, so the editor owns `label` as a
 *   reserved config key (kept out of generated schema forms, see
 *   ConfigPanel).
 * - Edge ids are deterministic (`from:port->to:port`) so bodies survive
 *   load/save round-trips without id churn.
 * - Port-type compatibility: equal port-type ids, unless the catalog's
 *   port-type entry carries an explicit compatibility list (see
 *   {@link portsCompatible}).
 */
import type { Connection, Edge, Node } from '@xyflow/react'
import type {
  NodeType,
  PipelineBody,
  PipelineEdge,
  PipelineIssue,
  PipelineNode,
  StudioCatalog,
  StudioDef,
  ValidationSeverity,
} from '../../../api/studio'
import { emptyValueFor } from '../../../components/SchemaForm'

/** RF node type id used for every pipeline node. */
export const RF_NODE_TYPE = 'wc-pipeline'

export interface PipelineNodeData extends Record<string, unknown> {
  nodeType: string
  config: Record<string, unknown>
  /** Error-severity messages from the last validation (danger ring on the node). */
  issues: string[]
}

export type PipelineRFNode = Node<PipelineNodeData>

export interface PipelineEdgeData extends Record<string, unknown> {
  /** `true` for the Gate `continue` loop edge. */
  loop: boolean
}

export type PipelineRFEdge = Edge<PipelineEdgeData>

/** Catalog + agent refs shared by the node view and the config panel. */
export interface EditorRefs {
  catalog: StudioCatalog | null
  agents: StudioDef<'agent'>[]
}

/* ---------- small helpers ---------- */

export function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** Deterministic edge id — duplicate wires naturally collapse to one id. */
export function edgeId(from: { node: string; port: string }, to: { node: string; port: string }): string {
  return `${from.node}:${from.port}->${to.node}:${to.port}`
}

/** Auto node id per type: `agent-1`, `agent-2`, … */
export function makeNodeId(type: string, existing: ReadonlySet<string>): string {
  let i = 1
  while (existing.has(`${type}-${i}`)) i++
  return `${type}-${i}`
}

/** Seed body for a new pipeline: one Input and one Output node, unconnected. */
export function emptyPipelineBody(): PipelineBody {
  return {
    nodes: [
      { id: 'input', type: 'input', config: {}, position: { x: 80, y: 200 } },
      { id: 'output', type: 'output', config: {}, position: { x: 640, y: 200 } },
    ],
    edges: [],
  }
}

/** Node display label — a reserved `config.label` key (see module docs). */
export function nodeLabel(data: PipelineNodeData): string {
  const v = data.config['label']
  return typeof v === 'string' ? v : ''
}

/** Title shown in the node header: label → agent name → node-type name. */
export function nodeTitle(data: PipelineNodeData, refs: EditorRefs): string {
  const label = nodeLabel(data)
  if (label !== '') return label
  if (data.nodeType === 'agent') {
    const agent = refs.agents.find((a) => a.id === data.config['agent_id'])
    if (agent) return agent.name
  }
  return nodeTypeOf(refs.catalog, data.nodeType)?.name ?? data.nodeType
}

/* ---------- catalog helpers ---------- */

export function nodeTypeOf(catalog: StudioCatalog | null | undefined, type: string): NodeType | undefined {
  return catalog?.node_types.find((t) => t.type === type) ?? undefined
}

export function portTypeLabel(catalog: StudioCatalog | null | undefined, typeId: string): string {
  return catalog?.port_types.find((p) => p.id === typeId)?.label ?? typeId
}

/** Extra catalog fields a port-type entry may use to declare compatible types. */
const COMPAT_KEYS = ['compatible_with', 'accepts', 'targets', 'connects_to'] as const

/**
 * Port-type compatibility (catalog is authoritative). When the SOURCE
 * port-type entry carries an explicit compatibility list (an extra catalog
 * field — `compatible_with`/`accepts`/`targets`/`connects_to`), that list
 * decides; otherwise compatibility means equal port-type ids.
 */
export function portsCompatible(catalog: StudioCatalog | null | undefined, srcType: string, dstType: string): boolean {
  if (srcType === dstType) return true
  const entry = catalog?.port_types.find((p) => p.id === srcType)
  if (!entry) return false
  const rec = entry as unknown as Record<string, unknown>
  for (const key of COMPAT_KEYS) {
    const v = rec[key]
    if (Array.isArray(v)) return v.map(String).includes(dstType)
  }
  return false
}

/** Initial config for a new node: schema empties seeded, backend defaults win. */
export function seedConfig(nt: NodeType): Record<string, unknown> {
  const base = emptyValueFor(nt.config_schema)
  const seed = isObj(base) ? base : {}
  return { ...seed, ...(nt.defaults ?? {}) }
}

/* ---------- body ⇄ flow ---------- */

/** Defensive parse of an (untrusted) PipelineBody coming from the API. */
export function sanitizeBody(v: unknown): PipelineBody {
  const o = isObj(v) ? v : {}
  const rawNodes = Array.isArray(o.nodes) ? o.nodes.filter(isObj) : []
  const rawEdges = Array.isArray(o.edges) ? o.edges.filter(isObj) : []
  const nodes: PipelineNode[] = rawNodes
    .map((n) => ({
      id: typeof n.id === 'string' ? n.id : '',
      type: typeof n.type === 'string' ? n.type : '',
      config: isObj(n.config) ? { ...n.config } : {},
      position: {
        x: Number(isObj(n.position) ? n.position.x : 0) || 0,
        y: Number(isObj(n.position) ? n.position.y : 0) || 0,
      },
    }))
    .filter((n) => n.id !== '' && n.type !== '')
  const edges: PipelineEdge[] = rawEdges
    .map((e) => {
      const from = isObj(e.from) ? e.from : {}
      const to = isObj(e.to) ? e.to : {}
      const f = { node: typeof from.node === 'string' ? from.node : '', port: typeof from.port === 'string' ? from.port : '' }
      const t = { node: typeof to.node === 'string' ? to.node : '', port: typeof to.port === 'string' ? to.port : '' }
      return { id: typeof e.id === 'string' && e.id !== '' ? e.id : edgeId(f, t), from: f, to: t, ...(e.loop === true ? { loop: true as const } : {}) }
    })
    .filter((e) => e.from.node !== '' && e.to.node !== '')
  return { nodes, edges }
}

/** Dashed animated rendering + a small `loop` label for Gate continue wires. */
function styleLoopEdge(edge: PipelineRFEdge): PipelineRFEdge {
  edge.animated = true
  edge.label = 'loop'
  edge.labelStyle = { fill: 'var(--color-ink-3)', fontSize: 10, fontFamily: 'var(--font-mono)' }
  edge.labelBgStyle = { fill: 'var(--color-surface)' }
  edge.labelBgPadding = [4, 2]
  edge.labelBgBorderRadius = 4
  return edge
}

export function bodyToFlow(body: unknown): { nodes: PipelineRFNode[]; edges: PipelineRFEdge[] } {
  const b = sanitizeBody(body)
  return {
    nodes: b.nodes.map((n) => ({
      id: n.id,
      type: RF_NODE_TYPE,
      position: { x: n.position.x, y: n.position.y },
      data: { nodeType: n.type, config: n.config, issues: [] },
    })),
    edges: b.edges.map((e) => {
      const edge: PipelineRFEdge = {
        id: e.id,
        source: e.from.node,
        target: e.to.node,
        sourceHandle: e.from.port || null,
        targetHandle: e.to.port || null,
        data: { loop: e.loop === true },
      }
      return e.loop ? styleLoopEdge(edge) : edge
    }),
  }
}

export function flowToBody(nodes: PipelineRFNode[], edges: PipelineRFEdge[]): PipelineBody {
  return {
    nodes: nodes
      .filter((n) => n.type === RF_NODE_TYPE)
      .map((n) => ({
        id: n.id,
        type: n.data.nodeType,
        config: isObj(n.data.config) ? { ...n.data.config } : {},
        position: { x: n.position.x, y: n.position.y },
      })),
    edges: edges.map((e) => ({
      id: e.id,
      from: { node: e.source, port: e.sourceHandle ?? '' },
      to: { node: e.target, port: e.targetHandle ?? '' },
      ...(e.data?.loop ? { loop: true } : {}),
    })),
  }
}

/** Build the RF edge for an accepted connection. */
export function makeEdge(c: Connection, loop: boolean): PipelineRFEdge {
  const from = { node: c.source ?? '', port: c.sourceHandle ?? '' }
  const to = { node: c.target ?? '', port: c.targetHandle ?? '' }
  const edge: PipelineRFEdge = {
    id: edgeId(from, to),
    source: from.node,
    target: to.node,
    sourceHandle: from.port || null,
    targetHandle: to.port || null,
    data: { loop },
  }
  return loop ? styleLoopEdge(edge) : edge
}

/* ---------- wire rules (plan §4.5) ---------- */

/** plan §4.5: the Gate's loop output — the only port allowed to cycle back. */
export const LOOP_PORT = 'continue'

export interface ConnectionCheck {
  ok: boolean
  /** Human-readable rejection reason (shown near the toolbar). */
  reason?: string
  /** `true` when the connection is the Gate `continue` loop edge. */
  loop?: boolean
}

/**
 * Validates a candidate connection against the catalog and the current
 * graph: resolvable ports, compatible port types, one wire per input (unless
 * the port allows `multiple`), no duplicate wires, at most one loop edge,
 * and a DAG over regular edges (only Gate `continue` may loop).
 */
export function checkConnection(
  catalog: StudioCatalog | null | undefined,
  nodes: PipelineRFNode[],
  edges: PipelineRFEdge[],
  c: { source?: string | null; target?: string | null; sourceHandle?: string | null; targetHandle?: string | null },
): ConnectionCheck {
  if (!c.source || !c.target || !c.sourceHandle || !c.targetHandle) return { ok: false }
  const srcNode = nodes.find((n) => n.id === c.source)
  const dstNode = nodes.find((n) => n.id === c.target)
  if (!srcNode || !dstNode) return { ok: false, reason: 'Wire endpoint is no longer on the canvas' }
  if (srcNode.id === dstNode.id) return { ok: false, reason: 'A node cannot connect to itself' }
  const srcNT = nodeTypeOf(catalog, srcNode.data.nodeType)
  const dstNT = nodeTypeOf(catalog, dstNode.data.nodeType)
  if (!srcNT || !dstNT) return { ok: false, reason: 'Unknown node type — is the catalog up to date?' }
  if (srcNode.data.nodeType === 'output') return { ok: false, reason: 'Output nodes have no outgoing ports' }
  if (dstNode.data.nodeType === 'input') return { ok: false, reason: 'Input nodes have no incoming ports' }
  const srcPort = srcNT.outputs.find((p) => p.name === c.sourceHandle)
  const dstPort = dstNT.inputs.find((p) => p.name === c.targetHandle)
  if (!srcPort || !dstPort) return { ok: false, reason: `Unknown port “${!srcPort ? c.sourceHandle : c.targetHandle}”` }
  if (!portsCompatible(catalog, srcPort.type, dstPort.type)) {
    return {
      ok: false,
      reason: `Incompatible port types — ${srcPort.name} (${portTypeLabel(catalog, srcPort.type)}) → ${dstPort.name} (${portTypeLabel(catalog, dstPort.type)})`,
    }
  }
  if (
    edges.some(
      (e) => e.source === c.source && e.sourceHandle === c.sourceHandle && e.target === c.target && e.targetHandle === c.targetHandle,
    )
  ) {
    return { ok: false, reason: 'These ports are already wired' }
  }
  if (!dstPort.multiple && edges.some((e) => e.target === c.target && e.targetHandle === c.targetHandle)) {
    return { ok: false, reason: `“${dstPort.name}” already has an incoming wire` }
  }
  const loop = srcNode.data.nodeType === 'gate' && c.sourceHandle === LOOP_PORT
  if (loop && edges.some((e) => e.data?.loop)) {
    return { ok: false, reason: 'Only one loop edge is allowed — the Gate continue wire already loops' }
  }
  if (!loop && createsCycle(edges, c.source, c.target)) {
    return { ok: false, reason: 'This wire would create a cycle — only the Gate continue port may loop back' }
  }
  return { ok: true, loop }
}

/** Cycle test over the regular (non-loop) edges plus a candidate from→to wire. */
export function createsCycle(edges: PipelineRFEdge[], from: string, to: string): boolean {
  if (from === to) return true
  const adj = new Map<string, string[]>()
  const push = (a: string, b: string) => {
    const list = adj.get(a)
    if (list) list.push(b)
    else adj.set(a, [b])
  }
  for (const e of edges) if (!e.data?.loop) push(e.source, e.target)
  push(from, to)
  const seen = new Set<string>()
  const stack = [to]
  while (stack.length > 0) {
    const n = stack.pop()
    if (n === undefined) continue
    if (n === from) return true
    if (seen.has(n)) continue
    seen.add(n)
    for (const m of adj.get(n) ?? []) stack.push(m)
  }
  return false
}

/* ---------- client-side lint (merged into Validate) ---------- */

/** Lightweight structural checks that run even without a reachable backend. */
export function lintBody(body: PipelineBody, catalog: StudioCatalog | null | undefined): PipelineIssue[] {
  const issues: PipelineIssue[] = []
  const add = (node_id: string, message: string, severity: ValidationSeverity) => issues.push({ node_id, message, severity })

  const ids = new Set<string>()
  for (const n of body.nodes) {
    if (ids.has(n.id)) add(n.id, `Duplicate node id “${n.id}”`, 'error')
    ids.add(n.id)
    if (!nodeTypeOf(catalog, n.type)) add(n.id, `Unknown node type “${n.type}”`, 'error')
  }
  if (!body.nodes.some((n) => n.type === 'input')) add('', 'Pipeline has no Input node', 'error')
  if (!body.nodes.some((n) => n.type === 'output')) add('', 'Pipeline has no Output node', 'error')

  for (const e of body.edges) {
    const srcNode = body.nodes.find((n) => n.id === e.from.node)
    const dstNode = body.nodes.find((n) => n.id === e.to.node)
    if (!srcNode || !dstNode) {
      add(e.from.node, 'Wire references a missing node', 'error')
      continue
    }
    const srcNT = nodeTypeOf(catalog, srcNode.type)
    const dstNT = nodeTypeOf(catalog, dstNode.type)
    const srcPort = srcNT?.outputs.find((p) => p.name === e.from.port)
    const dstPort = dstNT?.inputs.find((p) => p.name === e.to.port)
    if (!srcPort || !dstPort) {
      add(srcNode.id, `Wire references a missing port (${e.from.port} → ${e.to.port})`, 'error')
      continue
    }
    if (!portsCompatible(catalog, srcPort.type, dstPort.type)) {
      add(srcNode.id, `“${srcPort.name}” → “${dstPort.name}”: incompatible port types`, 'error')
    }
    if (e.loop) {
      if (!(srcNode.type === 'gate' && e.from.port === LOOP_PORT)) {
        add(srcNode.id, `Only the Gate “${LOOP_PORT}” port may form a loop edge`, 'error')
      }
      continue // loop edges are exempt from the DAG and single-wire checks
    }
    if (!dstPort.multiple) {
      const dupes = body.edges.filter((x) => x.id !== e.id && x.to.node === e.to.node && x.to.port === e.to.port)
      if (dupes.length > 0) add(dstNode.id, `“${dstPort.name}” receives more than one wire`, 'error')
    }
  }

  for (const n of body.nodes) {
    const nt = nodeTypeOf(catalog, n.type)
    if (!nt) continue
    if (n.type !== 'input') {
      for (const p of nt.inputs) {
        if (!body.edges.some((e) => e.to.node === n.id && e.to.port === p.name)) {
          add(n.id, `Input port “${p.name}” is not wired`, 'warning')
        }
      }
    }
    if (n.type === 'output' && !body.edges.some((e) => e.to.node === n.id)) {
      add(n.id, 'Nothing is wired into the output', 'warning')
    }
    if (
      n.type === 'gate' &&
      nt.outputs.some((p) => p.name === LOOP_PORT) &&
      !body.edges.some((e) => e.from.node === n.id && e.from.port === LOOP_PORT)
    ) {
      add(n.id, `Gate “${LOOP_PORT}” is not wired — the pipeline runs once without looping`, 'warning')
    }
  }

  return issues
}

/** Server issues first; client lint issues deduped in behind them. */
export function mergeIssues(server: PipelineIssue[], local: PipelineIssue[]): PipelineIssue[] {
  const out = [...server]
  const seen = new Set(server.map((i) => `${i.node_id}|${i.severity}|${i.message}`))
  for (const i of local) {
    const key = `${i.node_id}|${i.severity}|${i.message}`
    if (!seen.has(key)) {
      seen.add(key)
      out.push(i)
    }
  }
  return out
}
