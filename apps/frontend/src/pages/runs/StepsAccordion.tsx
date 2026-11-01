/**
 * Node transcripts (plan §5 Runs UI): an Accordion of the steps of one
 * iteration — status, model, duration, usage, then output (mono, collapsed
 * with "show all"), error, pinned versions and a copyable session chip.
 */
import { useMemo } from 'react'
import { Activity } from 'lucide-react'
import { Accordion, AccordionItem, EmptyState } from '../../ui/Display'
import type { RunStep } from '../../api/runs'
import {
  CopyChip,
  MonoBlock,
  RunStatusBadge,
  formatDuration,
  formatTokens,
  jsonText,
  nodeIcon,
} from './shared'

function startedTs(s: RunStep): number {
  const t = s.started_at ? new Date(s.started_at).getTime() : NaN
  return Number.isFinite(t) ? t : Number.POSITIVE_INFINITY
}

export function StepsAccordion({
  steps,
  nodeLabels,
  nodeTypes,
  className,
}: {
  steps: RunStep[]
  nodeLabels: Map<string, string>
  /** node id → pipeline node type, for the per-node icon. */
  nodeTypes?: Map<string, string>
  className?: string
}) {
  const sorted = useMemo(
    () => [...steps].sort((a, b) => startedTs(a) - startedTs(b) || a.node_id.localeCompare(b.node_id)),
    [steps],
  )

  // Pre-open the first step that isn't finished yet (live progress), else none.
  const defaultOpen = useMemo(() => {
    const live = sorted.find((s) => s.status === 'running' || s.status === 'failed')
    return live ? [live.id] : []
  }, [sorted])

  if (sorted.length === 0) {
    return (
      <EmptyState
        icon={Activity}
        title="No steps recorded"
        description="Nothing was scheduled for this iteration yet."
      />
    )
  }

  return (
    <Accordion multiple defaultOpen={defaultOpen} className={className}>
      {sorted.map((s) => {
        const Icon = nodeIcon(nodeTypes?.get(s.node_id) ?? '')
        const label = nodeLabels.get(s.node_id) ?? s.node_id
        const pinned = Object.entries(s.pinned_versions ?? {})
          .map(([k, v]) => `${k} v${v}`)
          .join(' · ')
        return (
          <AccordionItem
            key={s.id}
            id={s.id}
            title={
              <span className="flex min-w-0 items-center gap-2">
                <Icon size={13} className="shrink-0 text-ink-3" />
                <span className="truncate">{label}</span>
                <RunStatusBadge status={s.status} />
              </span>
            }
            trail={
              <span className="flex shrink-0 items-center gap-2 text-2xs text-ink-3">
                {s.model && <span className="hidden font-mono sm:inline">{s.model}</span>}
                <span className="font-mono">{formatDuration(s.started_at, s.completed_at)}</span>
                {s.usage && (
                  <span className="font-mono">
                    {formatTokens(s.usage.input_tokens + s.usage.cached_input_tokens)} →{' '}
                    {formatTokens(s.usage.output_tokens)}
                  </span>
                )}
              </span>
            }
          >
            <div className="flex flex-col gap-2.5">
              {s.error && (
                <pre className="whitespace-pre-wrap break-words rounded-md bg-danger-soft p-2.5 font-mono text-xs leading-relaxed text-danger">
                  {s.error}
                </pre>
              )}
              {s.output != null && <MonoBlock text={jsonText(s.output)} />}
              {pinned && (
                <p className="font-mono text-2xs text-ink-3">
                  <span className="font-sans font-medium text-ink-2">Pinned:</span> {pinned}
                </p>
              )}
              {s.agent_session_id && <CopyChip value={s.agent_session_id} label="session" />}
              {!s.error && s.output == null && !pinned && !s.agent_session_id && (
                <p className="text-xs text-ink-3">No output recorded.</p>
              )}
            </div>
          </AccordionItem>
        )
      })}
    </Accordion>
  )
}
