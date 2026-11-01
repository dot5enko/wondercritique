/**
 * The one custom React Flow node used by the pipeline editor: icon + title +
 * type badge header, then input ports (left) and output ports (right) as
 * Handles with mono port-name labels styled from the catalog's port_types
 * metadata. Selection gets the accent ring; error issues from the last
 * validation get a danger border + dot.
 */
import { createContext, useContext } from 'react'
import { Handle, Position, type NodeProps, type NodeTypes } from '@xyflow/react'
import { Blocks, Bot, GitFork, GitMerge, Image, LogIn, LogOut, ShieldCheck, type LucideIcon } from 'lucide-react'
import { Badge, StatusDot, Tooltip, cx } from '../../../ui'
import type { NodePort, StudioCatalog } from '../../../api/studio'
import { RF_NODE_TYPE, nodeTitle, nodeTypeOf, portTypeLabel, type EditorRefs, type PipelineRFNode } from './flow'

/** Catalog + agent refs, provided once by the editor around the whole canvas. */
export const RefsContext = createContext<EditorRefs>({ catalog: null, agents: [] })

const NODE_ICONS: Record<string, LucideIcon> = {
  input: LogIn,
  agent: Bot,
  validate: ShieldCheck,
  render: Image,
  merge: GitMerge,
  gate: GitFork,
  output: LogOut,
}

/** Icon per pipeline node type (falls back to a generic block icon). */
export function nodeIcon(type: string): LucideIcon {
  return NODE_ICONS[type] ?? Blocks
}

/**
 * Single generic RF node type — pipeline node types ride in `data.nodeType`
 * (RF's built-in `input`/`output`/`default` types must stay free).
 */
export const nodeTypes: NodeTypes = { [RF_NODE_TYPE]: PipelineNodeView }

export function PipelineNodeView({ data, selected }: NodeProps<PipelineRFNode>) {
  const refs = useContext(RefsContext)
  const nt = nodeTypeOf(refs.catalog, data.nodeType)
  const inputs = nt?.inputs ?? []
  const outputs = nt?.outputs ?? []
  const issues = data.issues ?? []
  const title = nodeTitle(data, refs)
  const Icon = nodeIcon(data.nodeType)

  return (
    <div
      className={cx(
        'w-56 rounded-lg border bg-surface shadow-xs transition-colors duration-100',
        issues.length > 0
          ? 'border-danger ring-2 ring-danger/20'
          : selected
            ? 'border-accent ring-2 ring-accent/25'
            : 'border-line hover:border-line-strong',
      )}
    >
      <div className="flex items-center gap-2 border-b border-line px-2.5 py-2">
        <span className="grid size-6 shrink-0 place-items-center rounded-md bg-app text-ink-2 [&_svg]:size-3.5">
          <Icon />
        </span>
        <span className="min-w-0 flex-1 truncate text-base font-medium text-ink" title={title}>
          {title}
        </span>
        {issues.length > 0 && <StatusDot tone="danger" />}
        <Tooltip content={nt?.description ?? data.nodeType} side="top">
          <Badge tone={issues.length > 0 ? 'danger' : 'neutral'} className="shrink-0">
            {nt?.name ?? data.nodeType}
          </Badge>
        </Tooltip>
      </div>

      {(inputs.length > 0 || outputs.length > 0) && (
        <div className="flex items-start justify-between gap-4 py-1.5">
          <div className="flex min-w-0 flex-col gap-0.5">
            {inputs.map((p) => (
              <PortRow key={p.name} port={p} kind="target" accent={selected === true} catalog={refs.catalog} />
            ))}
          </div>
          <div className="flex min-w-0 flex-col items-end gap-0.5">
            {outputs.map((p) => (
              <PortRow key={p.name} port={p} kind="source" accent={selected === true} catalog={refs.catalog} />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

/** One port: a Handle on the node edge + a muted mono label. */
function PortRow({
  port,
  kind,
  accent,
  catalog,
}: {
  port: NodePort
  kind: 'target' | 'source'
  accent: boolean
  catalog: StudioCatalog | null
}) {
  return (
    <div className="relative flex h-5 items-center">
      <Handle
        type={kind}
        position={kind === 'target' ? Position.Left : Position.Right}
        id={port.name}
        data-port-type={port.type}
        // z-index keeps the handle hit-area above the (positioned) label tooltip wrapper.
        className={cx('z-10! size-2.5! rounded-full! border bg-surface', accent ? 'border-accent!' : 'border-line-strong!')}
      />
      <Tooltip
        content={`${port.name} · ${portTypeLabel(catalog, port.type)}${port.multiple ? ' · multiple' : ''}`}
        side={kind === 'target' ? 'right' : 'top'}
      >
        <span className={cx('max-w-24 cursor-default truncate font-mono text-2xs text-ink-3', kind === 'target' ? 'pl-3.5' : 'pr-3.5')}>
          {port.name}
        </span>
      </Tooltip>
    </div>
  )
}
