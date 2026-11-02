/**
 * Agent editor (plan §4.4): sticky VersionBar above the main form and a
 * header "Try it" button that opens the Try-it modal. The
 * draft lives in useDirtyDraft with a debounced autosave to PUT draft; Try it
 * / preview flush it first so the backend sees what the user sees.
 */
import { useCallback, useState } from 'react'
import { Play } from 'lucide-react'
import { ErrorState, PageHeader, VersionBar } from '../../../components'
import { Button, Skeleton } from '../../../ui'
import { useAsync, useDirtyDraft } from '../../../hooks'
import {
  getCatalog,
  getEditableDef,
  putDraft,
  type AgentBody,
  type StudioCatalog,
  type StudioDef,
} from '../../../api/studio'
import { AgentForm } from './AgentForm'
import { TryItPanel } from './TryItPanel'
import { normalizeAgentBody } from './defaults'

export function AgentEditor({ id }: { id: string }) {
  const def = useAsync((signal) => getEditableDef('agent', id, signal), [id])
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
  const dd = useDirtyDraft<AgentBody>(normalizeAgentBody(def.draft), {
    autosave: (body) => putDraft('agent', def.id, body),
  })
  const [tryOpen, setTryOpen] = useState(false)
  const flushDraft = useCallback(() => dd.save((body) => putDraft('agent', def.id, body)), [dd.save, def.id])

  return (
    <>
      <VersionBar
        kind="agent"
        def={def}
        draft={dd.draft}
        dirty={dd.dirty}
        onDraftLoaded={(body) => dd.reset(normalizeAgentBody(body as AgentBody | null))}
        onSaved={onReloadDef}
        className="sticky top-0 z-30"
      />
      <div className="w-full flex-1 p-5">
        <PageHeader
          back={{ to: '/studio/agents', label: 'Agents' }}
          title="Agent"
          description="Configure the model, prompts, tools and output contract, then test the draft with Try it."
          actions={
            <Button variant="primary" icon={<Play />} onClick={() => setTryOpen(true)}>
              Try it
            </Button>
          }
        />
        <div className="mt-3">
          <AgentForm def={def} catalog={catalog} dd={dd} flushDraft={flushDraft} onReloadDef={onReloadDef} />
        </div>
        <TryItPanel
          open={tryOpen}
          onClose={() => setTryOpen(false)}
          def={def}
          catalog={catalog}
          dirty={dd.dirty}
          flushDraft={flushDraft}
        />
      </div>
    </>
  )
}

function EditorSkeleton() {
  return (
    <div className="w-full p-5" aria-busy="true">
      <Skeleton className="h-3.5 w-20" />
      <Skeleton className="mt-2 h-6 w-64" />
      <div className="mt-5 space-y-2">
        <Skeleton className="h-40" />
        <Skeleton className="h-64" />
        <Skeleton className="h-40" />
      </div>
    </div>
  )
}
