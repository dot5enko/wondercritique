/**
 * Transcript rendering — maps stored messages and the live streaming turn to
 * chat rows (plan §6): user bubbles right (accent-soft), assistant text left
 * as a borderless full-width markdown block, tool calls as collapsible rows,
 * images as rounded <img>, reasoning as a collapsed muted row. Timestamps on
 * hover.
 */
import { useMemo } from 'react'
import { RotateCw, Wrench } from 'lucide-react'
import { Badge, Button, Callout, Collapsible, Spinner } from '../../ui'
import { cx } from '../../ui'
import { Markdown, absoluteTime } from '../../components'
import type { AgentBlock } from '../../api'
import type { ChatEntry, LiveTurn } from './run'

/** Tool output resolved from the matching `tool_result` block (history) or the live call. */
export interface ToolOutput {
  output: string
  isError?: boolean
}

export function TranscriptList({
  entries,
  live,
  onRetry,
}: {
  entries: ChatEntry[]
  live: LiveTurn | null
  /** Re-runs a failed turn; the argument is the failed entry's input (undefined = live turn). */
  onRetry: (entryInput: string | undefined) => void
}) {
  // tool_result blocks live in role:"tool" messages — fold them into their
  // tool_call row by id so history renders like the live stream did.
  const outputs = useMemo(() => {
    const map = new Map<string, ToolOutput>()
    for (const e of entries) {
      for (const b of e.msg.message.blocks) {
        if (b.type === 'tool_result' && b.tool_call_id) {
          map.set(b.tool_call_id, { output: b.output ?? '', isError: b.is_error })
        }
      }
    }
    return map
  }, [entries])

  return (
    <div className="space-y-3">
      {entries.map((e) => (
        <MessageRow key={e.msg.id} entry={e} outputs={outputs} onRetry={onRetry} />
      ))}
      {live && <LiveRow live={live} onRetry={onRetry} />}
    </div>
  )
}

function MessageRow({
  entry,
  outputs,
  onRetry,
}: {
  entry: ChatEntry
  outputs: Map<string, ToolOutput>
  onRetry: (entryInput: string | undefined) => void
}) {
  const m = entry.msg
  if (m.message.role === 'user') {
    const text = m.message.blocks
      .filter((b) => b.type === 'text')
      .map((b) => b.text ?? '')
      .join('\n')
    if (!text) return null
    return (
      <div className="group flex flex-col items-end gap-0.5">
        <div className="max-w-full whitespace-pre-wrap break-words rounded-lg bg-accent-soft px-3 py-2 text-ink">{text}</div>
        <Timestamp iso={m.created_at} />
      </div>
    )
  }
  if (m.message.role !== 'assistant') return null // tool/system messages render via their tool rows
  return (
    <div className="group flex flex-col items-start gap-0.5">
      <AssistantBody blocks={m.message.blocks} outputs={outputs} />
      <Timestamp iso={m.created_at} />
      {entry.interrupted && <p className="text-2xs text-ink-3">Stopped — partial answer kept.</p>}
      {entry.failed && <FailedCallout error={entry.failed} onRetry={() => onRetry(entry.input)} />}
    </div>
  )
}

function AssistantBody({ blocks, outputs }: { blocks: AgentBlock[]; outputs: Map<string, ToolOutput> }) {
  const visible = blocks.filter((b) => b.type !== 'tool_result')
  const onlyText = visible.every((b) => b.type === 'text')
  if (onlyText) {
    const text = visible
      .map((b) => b.text ?? '')
      .filter(Boolean)
      .join('\n')
    if (!text) return null
    return <TextBubble text={text} />
  }
  return (
    <div className="flex w-full flex-col items-start gap-2">
      {visible.map((b, i) => {
        if (b.type === 'text') return b.text ? <TextBubble key={i} text={b.text} /> : null
        if (b.type === 'image') return <ImageBlock key={i} block={b} />
        if (b.type === 'tool_call') {
          const resolved =
            outputs.get(b.tool_call_id ?? '') ??
            (b.output != null ? { output: b.output, isError: b.is_error } : undefined)
          return (
            <ToolRow
              key={b.tool_call_id ?? i}
              data={{
                id: b.tool_call_id ?? `t${i}`,
                name: b.tool_name ?? 'tool',
                input: b.input,
                output: resolved?.output,
                isError: resolved?.isError,
              }}
            />
          )
        }
        if (b.type === 'reasoning') return b.text ? <ReasoningRow key={i} text={b.text} /> : null
        return null
      })}
    </div>
  )
}

function TextBubble({ text }: { text: string }) {
  // Borderless full-width block — markdown-rendered (raw user input in the
  // accent bubble above stays plain text). `w-full` keeps the block stretched
  // inside the items-start flex column instead of shrinking to content width.
  return <Markdown text={text} className="w-full text-ink" />
}

function ImageBlock({ block }: { block: AgentBlock }) {
  const url = block.image_url
  if (!url || !(url.startsWith('data:') || url.startsWith('https://'))) return null
  return <img src={url} alt="Agent image" className="max-h-80 max-w-sm rounded-lg border border-line object-contain" />
}

interface ToolRowData {
  id: string
  name: string
  input?: unknown
  output?: string
  isError?: boolean
  /** Live only: call started but not finished yet. */
  running?: boolean
  durationNs?: number
}

function ToolRow({ data }: { data: ToolRowData }) {
  let input = '{}'
  try {
    input = data.input === undefined ? '{}' : (JSON.stringify(data.input, null, 2) ?? '{}')
  } catch {
    input = String(data.input)
  }
  return (
    <Collapsible
      className="w-full"
      title={
        <span className="flex min-w-0 items-center gap-1.5">
          <Wrench size={13} className="shrink-0 text-ink-3" />
          <span className="truncate font-mono text-xs">{data.name}</span>
        </span>
      }
      trail={
        <>
          {data.running && <Spinner size={12} className="shrink-0 text-accent" />}
          {!data.running && data.durationNs != null && (
            <span className="shrink-0 text-2xs text-ink-3">{formatDuration(data.durationNs)}</span>
          )}
          {data.isError && <Badge tone="danger">error</Badge>}
        </>
      }
    >
      <div className="space-y-2">
        <pre className="max-h-44 overflow-y-auto whitespace-pre-wrap break-all rounded bg-app p-2 font-mono text-2xs text-ink-2">
          {input}
        </pre>
        {data.output != null && data.output !== '' && (
          <pre
            className={cx(
              'max-h-60 overflow-y-auto whitespace-pre-wrap break-words rounded bg-app p-2 font-mono text-2xs',
              data.isError ? 'text-danger' : 'text-ink-2',
            )}
          >
            {data.output}
          </pre>
        )}
      </div>
    </Collapsible>
  )
}

function ReasoningRow({ text, live = false }: { text: string; live?: boolean }) {
  return (
    <Collapsible
      className="w-full"
      title={<span className="text-base font-normal italic text-ink-3">Thinking…</span>}
      trail={live ? <Spinner size={12} className="shrink-0 text-ink-3" /> : undefined}
    >
      <div className="whitespace-pre-wrap break-words text-xs leading-relaxed text-ink-3">{text}</div>
    </Collapsible>
  )
}

function LiveRow({ live, onRetry }: { live: LiveTurn; onRetry: (entryInput: string | undefined) => void }) {
  const idle = live.parts.length === 0 && !live.reasoning && !live.failed
  return (
    <div className="flex flex-col items-start gap-2">
      {live.reasoning && <ReasoningRow text={live.reasoning} live={live.parts.length === 0} />}
      {live.parts.map((p, i) =>
        p.kind === 'text' ? (
          p.text ? (
            <TextBubble key={i} text={p.text} />
          ) : null
        ) : (
          <ToolRow
            key={p.call.id}
            data={{
              id: p.call.id,
              name: p.call.name,
              input: p.call.input,
              output: p.call.output,
              isError: p.call.isError,
              running: !p.call.done,
              durationNs: p.call.durationNs,
            }}
          />
        ),
      )}
      {idle && !live.failed && (
        <div className="flex items-center gap-2 rounded-lg border border-line bg-surface px-3 py-2 text-ink-3">
          <Spinner size={13} />
          <span className="text-base">Running…</span>
        </div>
      )}
      {live.failed && <FailedCallout error={live.failed} onRetry={() => onRetry(undefined)} />}
    </div>
  )
}

function FailedCallout({ error, onRetry }: { error: string; onRetry: () => void }) {
  return (
    <Callout tone="danger" title="Run failed" className="w-full">
      <div className="mt-1 flex items-center gap-2">
        <span className="min-w-0 break-words">{error}</span>
        <Button size="sm" variant="secondary" icon={<RotateCw />} className="shrink-0" onClick={onRetry}>
          Retry
        </Button>
      </div>
    </Callout>
  )
}

function Timestamp({ iso }: { iso: string }) {
  return (
    <span className="text-2xs text-ink-3 opacity-0 transition-opacity duration-100 group-hover:opacity-100">
      {absoluteTime(iso)}
    </span>
  )
}

function formatDuration(ns: number): string {
  const ms = ns / 1e6
  if (ms < 1) return '<1ms'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}
