/**
 * Agent editor (plan §4.4): sticky VersionBar above two columns — the main
 * form (≈2fr) and the "Try it" panel (≈1fr, stacked on narrow screens). The
 * draft lives in useDirtyDraft with a debounced autosave to PUT draft; Try it
 * / preview flush it first so the backend sees what the user sees.
 */
import { useCallback } from 'react'
import { ErrorState, PageHeader, VersionBar } from '../../../components'
import { Skeleton } from '../../../ui'
import { useAsync, useDirtyDraft } from '../../../hooks'
import {
  getCatalog,
  getDef,
  putDraft,
  type AgentBody,
  type StudioCatalog,
  type StudioDef,
} from '../../../api/studio'
import { AgentForm } from './AgentForm'
import { TryItPanel } from './TryItPanel'
import { sensibleDefaultAgentBody } from './defaults'

export function AgentEditor({ id }: { id: string }) {
  const def = useAsync((signal) => getDef('agent', id, signal), [id])
  const catalog = useAsync((signal) => getCatalog(signal), [])

  const ready = def.data != null && catalog.data != null
  const error = ready ? null : (def.error ?? catalog.error)

  return (
    <div className="flex min-h-full flex-col">
      {def.data != null && catalog.data != null ? (
        <EditorBody def={def.data} catalog={catalog.data} onReloadDef={def.reload} />
      ) : error != null ? (
        <div className="p-5">
          <PageHeader back={{ to: '/studio/agents', label: 'Agents' }} title="Agent" />
          <ErrorState
            error={error}
            onRetry={() => {
              def.reload()
              catalog.reload()
            }}
          />
        </div>
      ) : (
        <EditorSkeleton />
      )}
    </div>
  )
}

function EditorBody({
  def,
  catalog,
  onReloadDef,
}: {
  def: StudioDef<'agent'>
  catalog: StudioCatalog
  onReloadDef: () => void
}) {
  const dd = useDirtyDraft<AgentBody>(def.draft ?? sensibleDefaultAgentBody(), {
    autosave: (body) => putDraft('agent', def.id, body),
  })
  const flushDraft = useCallback(() => dd.save((body) => putDraft('agent', def.id, body)), [dd.save, def.id])

  return (
    <>
      <VersionBar
        kind="agent"
        def={def}
        draft={dd.draft}
        dirty={dd.dirty}
        onDraftLoaded={(body) => dd.reset((body as AgentBody | null) ?? sensibleDefaultAgentBody())}
        onSaved={onReloadDef}
        className="sticky top-0 z-30"
      />
      <div className="mx-auto w-full max-w-7xl flex-1 p-5">
        <PageHeader
          back={{ to: '/studio/agents', label: 'Agents' }}
          title={def.name}
          description={def.description || <span className="font-mono">{def.slug}</span>}
        />
        <div className="mt-4 grid items-start gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          <AgentForm def={def} catalog={catalog} dd={dd} flushDraft={flushDraft} />
          <TryItPanel def={def} catalog={catalog} dirty={dd.dirty} flushDraft={flushDraft} />
        </div>
      </div>
    </>
  )
}

function EditorSkeleton() {
  return (
    <div className="mx-auto w-full max-w-7xl p-5" aria-busy="true">
      <Skeleton className="h-3.5 w-20" />
      <Skeleton className="mt-2 h-6 w-64" />
      <div className="mt-5 grid items-start gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <div className="space-y-4">
          <Skeleton className="h-40" />
          <Skeleton className="h-64" />
          <Skeleton className="h-40" />
        </div>
        <Skeleton className="h-96" />
      </div>
    </div>
  )
}
