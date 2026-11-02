/**
 * Pipeline editor (plan.critique-engine.md §4.5): full-height React Flow
 * canvas on @xyflow/react v12, VersionBar on top, toolbar (back, add node,
 * validate, transient messages) and a right config panel for the selection.
 *
 * The React Flow state is the working source of truth; every change is
 * serialized back into the PipelineBody draft (useDirtyDraft → debounced
 * `putDraft` autosave). Wire rules — port-type compatibility, one wire per
 * input, single loop edge, DAG over regular edges — are enforced live in
 * `flow.ts::checkConnection`, wired both as `isValidConnection` (visual
 * feedback) and in `onConnect` (rejection message). Node deletions via the
 * panel button go through a ConfirmDialog; RF's delete key applies instantly.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import {
  Background,
  BackgroundVariant,
  ConnectionLineType,
  Controls,
  MarkerType,
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  useEdgesState,
  useNodesState,
  useReactFlow,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ArrowLeft, CircleAlert, CircleCheck, Plus, ShieldCheck, TriangleAlert, Workflow, X } from 'lucide-react'
import { Badge, Button, Collapsible, EmptyState, IconButton, Menu, cx } from '../../../ui'
import { Callout } from '../../../ui/Display'
import { ConfirmDialog, VersionBar } from '../../../components'
import {
  putDraft,
  validatePipeline,
  type NodeType,
  type PipelineBody,
  type PipelineIssue,
  type StudioCatalog,
  type StudioDef,
} from '../../../api/studio'
import { useDirtyDraft } from '../../../hooks'
import { ConfigPanel, type PanelTarget } from './ConfigPanel'
import { RefsContext, nodeIcon, nodeTypes } from './PipelineNodeView'
import {
  RF_NODE_TYPE,
  bodyToFlow,
  checkConnection,
  designLoopTemplate,
  emptyPipelineBody,
  flowToBody,
  lintBody,
  makeEdge,
  makeNodeId,
  mergeIssues,
  nodeTitle,
  sanitizeBody,
  seedConfig,
  type EditorRefs,
  type PipelineRFEdge,
  type PipelineRFNode,
} from './flow'

/** React Flow theming — every value references a design-system token. */
const RF_VARS = {
  '--xy-background-color': 'var(--color-surface)',
  '--xy-edge-stroke': 'var(--color-line-strong)',
  '--xy-edge-stroke-selected': 'var(--color-accent)',
  '--xy-edge-stroke-width': '1.5',
  '--xy-connectionline-stroke': 'var(--color-accent)',
  '--xy-connectionline-stroke-width': '1.5',
  '--xy-node-border': 'none',
  '--xy-node-background-color': 'transparent',
  '--xy-node-boxshadow-hover': 'none',
  '--xy-node-boxshadow-selected': 'none',
  '--xy-handle-background-color': 'var(--color-surface)',
  '--xy-handle-border-color': 'var(--color-line-strong)',
  '--xy-selection-background-color': 'color-mix(in oklab, var(--color-accent) 8%, transparent)',
  '--xy-selection-border': '1px dashed var(--color-accent-muted)',
  '--xy-controls-button-background-color': 'var(--color-surface)',
  '--xy-controls-button-background-color-hover': 'var(--color-app)',
  '--xy-controls-button-color': 'var(--color-ink-2)',
  '--xy-controls-button-color-hover': 'var(--color-ink)',
  '--xy-controls-button-border-color': 'var(--color-line)',
  '--xy-controls-box-shadow': 'var(--shadow-pop)',
  '--xy-attribution-background-color': 'transparent',
} as CSSProperties

const FLASH_TONES: Record<Flash['tone'], string> = {
  danger: 'text-danger',
  warning: 'text-warning',
  success: 'text-success',
}

interface Flash {
  tone: 'danger' | 'success' | 'warning'
  text: string
}

/** One-line explanation per node type, shown in the Add-node palette
 * (catalog description wins when present). */
const NODE_TYPE_HINTS: Record<string, string> = {
  input: 'Asks the user for the brief when a run starts.',
  agent: 'Runs one agent — writes, critiques, decides. Pick the agent in its config.',
  validate: 'Checks the HTML against the component rules.',
  render: 'Screenshots the HTML so vision agents can see it.',
  merge: 'Combines several critiques into one.',
  gate: 'Decides: loop back for another iteration, or finish. Configure the score threshold and max iterations.',
  output: 'Where the finished result leaves the pipeline.',
}

interface ValidationResult {
  issues: PipelineIssue[]
  hasErrors: boolean
}

export interface PipelineEditorProps {
  def: StudioDef<'pipeline'>
  catalog: StudioCatalog
  agents: StudioDef<'agent'>[]
  onBack: () => void
  /** Refetches the def after save/publish so version badges stay fresh. */
  onDefSaved: () => void
}

/** Drops stale per-node validation issues whenever the graph changes. */
function stripIssues(nodes: PipelineRFNode[]): PipelineRFNode[] {
  return nodes.some((n) => n.data.issues.length > 0)
    ? nodes.map((n) => (n.data.issues.length > 0 ? { ...n, data: { ...n.data, issues: [] } } : n))
    : nodes
}

export default function PipelineEditor(props: PipelineEditorProps) {
  return (
    <ReactFlowProvider>
      <EditorBody {...props} />
    </ReactFlowProvider>
  )
}

function EditorBody({ def, catalog, agents, onBack, onDefSaved }: PipelineEditorProps) {
  const refs = useMemo<EditorRefs>(() => ({ catalog, agents }), [catalog, agents])

  const { draft, setDraft, dirty, saveState, reset, save } = useDirtyDraft<PipelineBody>(def.draft ?? emptyPipelineBody(), {
    autosave: (body) => putDraft('pipeline', def.id, body),
  })

  const boot = useMemo(() => bodyToFlow(def.draft ?? emptyPipelineBody()), []) // restore re-runs via handleDraftLoaded
  const [nodes, setNodes] = useNodesState<PipelineRFNode>(boot.nodes)
  const [edges, setEdges] = useEdgesState<PipelineRFEdge>(boot.edges)
  const nodesRef = useRef(nodes)
  nodesRef.current = nodes
  const edgesRef = useRef(edges)
  edgesRef.current = edges

  const rf = useReactFlow<PipelineRFNode, PipelineRFEdge>()
  const canvasRef = useRef<HTMLDivElement>(null)

  /* ---------- transient toolbar messages ---------- */

  const [flash, setFlash] = useState<Flash | null>(null)
  const flashRef = useRef<Flash | null>(null)
  const showFlash = useCallback((tone: Flash['tone'], text: string) => {
    if (flashRef.current?.text === text && flashRef.current?.tone === tone) return
    flashRef.current = { tone, text }
    setFlash({ tone, text })
  }, [])
  useEffect(() => {
    if (!flash) return
    const t = window.setTimeout(() => {
      flashRef.current = null
      setFlash(null)
    }, 4200)
    return () => window.clearTimeout(t)
  }, [flash])

  /* ---------- RF state → draft ---------- */

  const commit = useCallback(
    (nextNodes: PipelineRFNode[], nextEdges: PipelineRFEdge[]) => {
      const cleaned = stripIssues(nextNodes)
      setNodes(cleaned)
      setDraft(flowToBody(cleaned, nextEdges))
    },
    [setDraft, setNodes],
  )

  const handleNodeChanges = useCallback(
    (changes: NodeChange<PipelineRFNode>[]) => {
      const next = applyNodeChanges(changes, nodesRef.current)
      // In-flight drag positions stay local; the drag stop below commits the
      // final position straight from the RF store (the ref trails a render).
      const dragging = changes.length > 0 && changes.every((c) => c.type === 'position' && c.dragging === true)
      if (dragging) setNodes(next)
      else commit(next, edgesRef.current)
    },
    [commit, setNodes],
  )

  const handleEdgeChanges = useCallback(
    (changes: EdgeChange<PipelineRFEdge>[]) => {
      const next = applyEdgeChanges(changes, edgesRef.current)
      setEdges(next) // selection/removal changes must reach the React state too
      commit(nodesRef.current, next)
    },
    [commit, setEdges],
  )

  // Final position after a node drag — read from the RF store, which is
  // already up to date even when the React state has not re-rendered yet.
  const onNodeDragStop = useCallback(() => {
    commit(rf.getNodes(), edgesRef.current)
  }, [commit, rf])

  const onConnect = useCallback(
    (c: Connection) => {
      const check = checkConnection(catalog, nodesRef.current, edgesRef.current, c)
      if (!check.ok) {
        showFlash('danger', check.reason ?? 'Connection rejected')
        return
      }
      const nextEdges = [...edgesRef.current, makeEdge(c, check.loop === true)]
      setEdges(nextEdges)
      commit(nodesRef.current, nextEdges)
      flashRef.current = null // clear any stale rejection message from mid-drag hovers
      setFlash(null)
    },
    [catalog, commit, setEdges, setFlash, showFlash],
  )

  // Same rules as onConnect — powers the invalid-handle feedback while
  // dragging; the reason doubles as the rejection message (deduped).
  const isValidConnection = useCallback(
    (c: Connection | PipelineRFEdge) => {
      const check = checkConnection(catalog, nodesRef.current, edgesRef.current, c)
      if (!check.ok) {
        if (check.reason) showFlash('danger', check.reason)
        return false
      }
      return true
    },
    [catalog, showFlash],
  )

  /* ---------- node ops ---------- */

  const addNode = useCallback(
    (nt: NodeType) => {
      const id = makeNodeId(nt.type, new Set(nodesRef.current.map((n) => n.id)))
      const rect = canvasRef.current?.getBoundingClientRect()
      const position = rf.screenToFlowPosition({
        x: (rect ? rect.left + rect.width / 2 : window.innerWidth / 2) - 110,
        y: (rect ? rect.top + rect.height / 2 : window.innerHeight / 2) - 40,
      })
      const node: PipelineRFNode = { id, type: RF_NODE_TYPE, position, data: { nodeType: nt.type, config: seedConfig(nt), issues: [] } }
      const next = [...nodesRef.current, node]
      setNodes(next)
      commit(next, edgesRef.current)
    },
    [commit, rf, setNodes],
  )

  const updateNodeConfig = useCallback(
    (id: string, config: Record<string, unknown>) => {
      commit(
        nodesRef.current.map((n) => (n.id === id ? { ...n, data: { ...n.data, config } } : n)),
        edgesRef.current,
      )
    },
    [commit],
  )

  const deleteNode = useCallback(
    (id: string) => {
      const nextNodes = nodesRef.current.filter((n) => n.id !== id)
      const nextEdges = edgesRef.current.filter((e) => e.source !== id && e.target !== id)
      setEdges(nextEdges)
      commit(nextNodes, nextEdges)
    },
    [commit, setEdges],
  )

  const deleteEdge = useCallback(
    (id: string) => {
      const nextEdges = edgesRef.current.filter((e) => e.id !== id)
      setEdges(nextEdges)
      commit(nodesRef.current, nextEdges)
    },
    [commit, setEdges],
  )

  /* ---------- validation ---------- */

  const [validating, setValidating] = useState(false)
  const [validation, setValidation] = useState<ValidationResult | null>(null)
  const [issuesOpen, setIssuesOpen] = useState(false)

  const runValidation = useCallback(async () => {
    setValidating(true)
    try {
      // The backend validates the saved draft (else the latest published body) —
      // flush pending edits first, but never create a draft from a clean view.
      if (dirty) await save((body) => putDraft('pipeline', def.id, body))
      const body = flowToBody(nodesRef.current, edgesRef.current)
      const local = lintBody(body, catalog)
      let issues = local
      let serverError = false
      try {
        const res = await validatePipeline(def.id)
        issues = mergeIssues(Array.isArray(res.issues) ? res.issues : [], local)
      } catch {
        serverError = true
      }
      const hasErrors = issues.some((i) => i.severity === 'error')
      const errs = new Map<string, string[]>()
      for (const i of issues) {
        if (i.severity !== 'error' || i.node_id === '') continue
        errs.set(i.node_id, [...(errs.get(i.node_id) ?? []), i.message])
      }
      setNodes((ns) => ns.map((n) => ({ ...n, data: { ...n.data, issues: errs.get(n.id) ?? [] } })))
      if (issues.length === 0) {
        setValidation(null)
        showFlash('success', serverError ? 'Local checks pass — server validation unavailable.' : 'Pipeline is valid.')
      } else {
        setValidation({ issues, hasErrors })
        setIssuesOpen(true)
        if (serverError) showFlash('warning', 'Server validation unavailable — showing local checks only.')
      }
    } finally {
      setValidating(false)
    }
  }, [catalog, def.id, dirty, save, setNodes, showFlash])

  /* ---------- version restore ---------- */

  const handleDraftLoaded = useCallback(
    (body: unknown) => {
      reset(sanitizeBody(body))
      const flow = bodyToFlow(body)
      setNodes(flow.nodes)
      setEdges(flow.edges)
      setValidation(null)
      window.setTimeout(() => {
        void rf.fitView({ padding: 0.25, duration: 200 })
      }, 60)
    },
    [reset, rf, setEdges, setNodes],
  )

  /* ---------- selection / issue focus ---------- */

  const selectedNode = nodes.find((n) => n.selected) ?? null
  const selectedEdge = selectedNode ? null : (edges.find((e) => e.selected) ?? null)
  const panelTarget: PanelTarget = selectedNode
    ? { kind: 'node', node: selectedNode }
    : selectedEdge
      ? { kind: 'edge', edge: selectedEdge }
      : null

  /* ---------- help strip + template ---------- */

  const [helpOpen, setHelpOpen] = useState(() => {
    try {
      return localStorage.getItem('wc_pipeline_help_v1') == null
    } catch {
      return true
    }
  })
  const dismissHelp = useCallback(() => {
    setHelpOpen(false)
    try {
      localStorage.setItem('wc_pipeline_help_v1', '1')
    } catch {
      // private mode — the strip just reappears next visit
    }
  }, [])

  const insertTemplate = useCallback(() => {
    const body = designLoopTemplate(catalog, agents)
    if (!body) {
      showFlash('warning', 'Template unavailable — the catalog did not load.')
      return
    }
    const flow = bodyToFlow(body)
    setNodes(flow.nodes)
    setEdges(flow.edges)
    commit(flow.nodes, flow.edges)
    window.setTimeout(() => void rf.fitView({ padding: 0.2, duration: 300 }), 80)
  }, [agents, catalog, commit, rf, setEdges, setNodes, showFlash])

  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)
  const confirmDeleteTitle = confirmDeleteId ? nodeTitle(nodesRef.current.find((n) => n.id === confirmDeleteId)?.data ?? { nodeType: '?', config: {}, issues: [] }, refs) : ''

  const focusIssue = useCallback(
    (nodeId: string) => {
      setNodes((ns) => ns.map((n) => ({ ...n, selected: n.id === nodeId })))
      void rf.fitView({ nodes: [{ id: nodeId }], padding: 2, duration: 300, maxZoom: 1.25 })
    },
    [rf, setNodes],
  )

  return (
    <RefsContext.Provider value={refs}>
      <div className="flex h-full min-h-0 flex-col">
        <VersionBar kind="pipeline" def={def} draft={draft} dirty={dirty} onDraftLoaded={handleDraftLoaded} onSaved={onDefSaved} />

        {/* toolbar */}
        <div className="flex shrink-0 items-center gap-2 border-b border-line bg-surface px-3 py-1.5">
          <IconButton icon={<ArrowLeft />} label="Back to pipelines" size="sm" variant="ghost" onClick={onBack} />
          <div className="mr-1 min-w-0 leading-tight">
            <p className="truncate text-base font-medium text-ink">{def.name}</p>
            <p className="truncate font-mono text-2xs text-ink-3">{def.slug}</p>
          </div>
          <div className="h-5 w-px shrink-0 bg-line" />
          <Menu
            trigger={
              <Button size="sm" variant="secondary" icon={<Plus />}>
                Add node
              </Button>
            }
          >
            <Menu.Label>Node types</Menu.Label>
            {catalog.node_types.map((nt) => {
              const Icon = nodeIcon(nt.type)
              const hint = (typeof nt.description === 'string' && nt.description.trim()) || (NODE_TYPE_HINTS[nt.type] ?? '')
              return (
                <Menu.Item key={nt.type} icon={Icon} onClick={() => addNode(nt)}>
                  {nt.name}
                  {hint && <span className="ml-1 hidden text-2xs text-ink-3 lg:inline">— {hint}</span>}
                </Menu.Item>
              )
            })}
          </Menu>
          <Button size="sm" variant="secondary" icon={<ShieldCheck />} loading={validating} onClick={() => void runValidation()}>
            Validate
          </Button>
          {flash && (
            <span className={cx('inline-flex min-w-0 items-center gap-1.5 text-xs', FLASH_TONES[flash.tone])}>
              {flash.tone === 'success' ? <CircleCheck size={13} className="shrink-0" /> : flash.tone === 'warning' ? <TriangleAlert size={13} className="shrink-0" /> : <CircleAlert size={13} className="shrink-0" />}
              <span className="truncate">{flash.text}</span>
            </span>
          )}
          {saveState === 'error' && <span className="shrink-0 text-xs text-danger">Autosave failed — retry via “Save draft”.</span>}
          <span className="ml-auto shrink-0 text-2xs text-ink-3">
            {nodes.length} nodes · {edges.length} wires
          </span>
        </div>

        {/* validation issues */}
        {validation && validation.issues.length > 0 && (
          <Collapsible
            title={`Issues (${validation.issues.length})`}
            open={issuesOpen}
            onOpenChange={setIssuesOpen}
            trail={<Badge tone={validation.hasErrors ? 'danger' : 'warning'}>{validation.hasErrors ? 'Errors' : 'Warnings'}</Badge>}
            className="shrink-0 rounded-none border-x-0 border-t-0"
          >
            <ul className="flex flex-col gap-1.5">
              {validation.issues.map((issue, i) => (
                <li key={`${issue.node_id}-${i}`} className="flex items-start gap-2 text-xs">
                  <Badge tone={issue.severity === 'error' ? 'danger' : 'warning'} className="shrink-0 capitalize">
                    {issue.severity}
                  </Badge>
                  <span className="min-w-0 flex-1 text-ink-2">{issue.message}</span>
                  {issue.node_id !== '' && (
                    <button
                      type="button"
                      onClick={() => focusIssue(issue.node_id)}
                      className="shrink-0 font-mono text-2xs text-ink-3 transition-colors duration-100 hover:text-accent"
                    >
                      {nodeTitle(nodesRef.current.find((n) => n.id === issue.node_id)?.data ?? { nodeType: issue.node_id, config: {}, issues: [] }, refs)}
                    </button>
                  )}
                </li>
              ))}
            </ul>
          </Collapsible>
        )}

        {/* canvas + config panel */}
        <div className="flex min-h-0 flex-1">
          <div ref={canvasRef} className="relative min-w-0 flex-1" style={RF_VARS}>
            {helpOpen && (
              <div className="absolute left-1/2 top-3 z-10 w-[min(560px,90%)] -translate-x-1/2">
                <Callout tone="info" title="How the graph works" className="pr-8 shadow-md">
                  <ul className="mt-1 list-inside list-disc space-y-0.5 text-xs">
                    <li>Click a node to configure it; drag from an output port to an input port to wire.</li>
                    <li>Wires only connect compatible ports. Data flows left to right during each iteration.</li>
                    <li>The dashed animated wire is the loop: the Gate sends work back for another iteration until the score passes (or the limits hit).</li>
                    <li>Validate checks the graph; Publish makes it usable in the design chat.</li>
                  </ul>
                </Callout>
                <IconButton
                  icon={<X />}
                  label="Dismiss help"
                  size="sm"
                  variant="ghost"
                  className="absolute right-1 top-1"
                  onClick={dismissHelp}
                />
              </div>
            )}
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              onNodesChange={handleNodeChanges}
              onEdgesChange={handleEdgeChanges}
              onNodeDragStop={onNodeDragStop}
              onConnect={onConnect}
              isValidConnection={isValidConnection}
              deleteKeyCode={['Backspace', 'Delete']}
              defaultEdgeOptions={{ markerEnd: { type: MarkerType.ArrowClosed } }}
              connectionLineType={ConnectionLineType.Bezier}
              connectionRadius={28}
              fitView
              fitViewOptions={{ padding: 0.25 }}
              minZoom={0.2}
              maxZoom={1.75}
              className="h-full w-full"
            >
              <Background variant={BackgroundVariant.Dots} gap={18} size={1.4} color="var(--color-line)" />
              <Controls showInteractive={false} className="overflow-hidden rounded-md border border-line" />
            </ReactFlow>
            {nodes.length === 0 && (
              <div className="absolute inset-0 grid place-items-center">
                <div className="flex flex-col items-center gap-3 rounded-xl border border-line bg-surface p-6 text-center shadow-xs">
                  <EmptyState
                    icon={Workflow}
                    title="Empty canvas"
                    description="Start from the built-in design loop, or add nodes yourself."
                    className="py-0"
                  />
                  <div className="flex items-center gap-2">
                    <Button size="sm" variant="primary" onClick={insertTemplate}>
                      Use the design loop template
                    </Button>
                    <span className="text-2xs text-ink-3">or add nodes from the toolbar above</span>
                  </div>
                </div>
              </div>
            )}
          </div>
          <aside className="w-80 shrink-0 overflow-y-auto border-l border-line bg-surface">
            <ConfigPanel
              target={panelTarget}
              nodes={nodes}
              edges={edges}
              refs={refs}
              onNodeConfig={updateNodeConfig}
              onRequestNodeDelete={setConfirmDeleteId}
              onDeleteEdge={deleteEdge}
            />
          </aside>
        </div>

        <ConfirmDialog
          open={confirmDeleteId !== null}
          onClose={() => setConfirmDeleteId(null)}
          title="Delete node?"
          body={`Node “${confirmDeleteTitle}” and its wires will be removed from the draft.`}
          confirmLabel="Delete"
          danger
          onConfirm={() => {
            if (confirmDeleteId) deleteNode(confirmDeleteId)
            setConfirmDeleteId(null)
          }}
        />
      </div>
    </RefsContext.Provider>
  )
}
