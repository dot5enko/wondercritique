/**
 * Read-only mini pipeline graph for a run (plan §5 Runs UI).
 *
 * Self-contained on purpose — does not import from `pages/studio`. Renders the
 * pinned pipeline body with a small custom node (icon + label + per-step status
 * dot for the displayed iteration); nodes are not draggable and the view fits
 * itself. Loop edges (Gate `continue`) render dashed in the accent color.
 * All colors are CSS var strings from the design tokens.
 */
import { useMemo } from 'react'
import {
  Background,
  Handle,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
  type NodeTypes,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { cx } from '../../ui/cx'
import { StatusDot, type StatusTone } from '../../ui/Display'
import type { PipelineBody } from '../../api/studio'
import type { RunStatus } from '../../api/runs'
import { nodeIcon, nodeLabel } from './shared'

/** Step status shown on a node; `none` = no step recorded (no dot). */
export type RunNodeStepStatus = RunStatus | 'none'

interface RunNodeData extends Record<string, unknown> {
  label: string
  nodeType: string
  status: RunNodeStepStatus
}

type RunFlowNode = Node<RunNodeData, 'runNode'>

function NodeStatusDot({ status }: { status: RunNodeStepStatus }) {
  if (status === 'none') return null
  const tone: StatusTone =
    status === 'running'
      ? 'accent'
      : status === 'completed'
        ? 'success'
        : status === 'failed'
          ? 'danger'
          : status === 'pending'
            ? 'neutral'
            : 'warning' // cancelled / interrupted
  return <StatusDot tone={tone} pulse={status === 'running'} />
}

function RunNodeView({ data }: NodeProps<RunFlowNode>) {
  const Icon = nodeIcon(data.nodeType)
  return (
    <div
      className={cx(
        'flex min-w-0 max-w-44 items-center gap-1.5 rounded-lg border bg-surface px-2 py-1.5 text-xs font-medium text-ink shadow-xs transition-colors duration-100',
        data.status === 'running' ? 'border-accent ring-2 ring-accent/25' : 'border-line',
      )}
    >
      <Icon size={13} className="shrink-0 text-ink-3" />
      <span className="min-w-0 truncate">{data.label}</span>
      <NodeStatusDot status={data.status} />
      <Handle
        type="target"
        position={Position.Left}
        isConnectable={false}
        className="!size-1.5 !rounded-full !border-0 !bg-ink-3"
      />
      <Handle
        type="source"
        position={Position.Right}
        isConnectable={false}
        className="!size-1.5 !rounded-full !border-0 !bg-ink-3"
      />
    </div>
  )
}

const nodeTypes: NodeTypes = { runNode: RunNodeView }

export interface RunGraphProps {
  body: PipelineBody | null
  /** Step status per node id for the displayed iteration. */
  statusByNode: Record<string, RunNodeStepStatus>
  className?: string
}

export function RunGraph({ body, statusByNode, className }: RunGraphProps) {
  const nodes = useMemo<RunFlowNode[]>(
    () =>
      (body?.nodes ?? []).map((n) => ({
        id: n.id,
        type: 'runNode' as const,
        // defensive: bodies from third parties may omit positions (the editor always writes them)
        position: n.position ?? { x: 0, y: 0 },
        data: { label: nodeLabel(n), nodeType: n.type, status: statusByNode[n.id] ?? 'none' },
        draggable: false,
        selectable: false,
        connectable: false,
        deletable: false,
      })),
    [body, statusByNode],
  )

  const edges = useMemo<Edge[]>(
    () =>
      (body?.edges ?? []).map((e) => ({
        id: e.id,
        source: e.from.node,
        target: e.to.node,
        type: 'smoothstep',
        style: e.loop
          ? { stroke: 'var(--color-accent)', strokeWidth: 1.5, strokeDasharray: '5 4' }
          : { stroke: 'var(--color-line-strong)', strokeWidth: 1.5 },
      })),
    [body],
  )

  // Refit the view when the underlying body changes (e.g. version fetch lands).
  const fitKey = useMemo(() => (body?.nodes ?? []).map((n) => n.id).join('|'), [body])

  if (nodes.length === 0) {
    return (
      <div className={cx('grid place-items-center text-xs text-ink-3', className)}>
        Pipeline graph unavailable
      </div>
    )
  }

  return (
    <div className={cx('h-full w-full', className)}>
      <ReactFlow
        key={fitKey}
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        edgesFocusable={false}
        zoomOnScroll={false}
        panOnScroll={false}
        zoomOnDoubleClick={false}
        preventScrolling={false}
        minZoom={0.2}
        maxZoom={1.4}
        fitView
        fitViewOptions={{ padding: 0.12, maxZoom: 1 }}
        colorMode="light"
      >
        <Background color="var(--color-line-strong)" gap={18} size={1} />
      </ReactFlow>
    </div>
  )
}
