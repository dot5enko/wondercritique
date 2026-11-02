/**
 * Model-set editor (plan §3.4): strategy + ordered model entries with
 * capability-filtered selection and per-entry params. The body form is bound
 * to useDirtyDraft (debounced autosave via putDraft); draft status, publishing
 * and version history/diff/restore live in VersionBar.
 */
import { useEffect, useState } from 'react'
import { useAsync, useDirtyDraft } from '../../../hooks'
import type { UseAsyncResult } from '../../../hooks'
import { getEditableDef, putDraft } from '../../../api/studio'
import type { ModelSetBody, StudioCatalog } from '../../../api/studio'
import { ErrorState, IdentityCard, PageHeader, VersionBar } from '../../../components'
import { Button, Callout, Card, CardBody, CardHeader, Checkbox, Segmented, Skeleton } from '../../../ui'
import { EntryList } from './entries'

const EMPTY_BODY: ModelSetBody = { strategy: 'single', entries: [] }

const STRATEGY_HINTS: Record<ModelSetBody['strategy'], string> = {
  single: 'single: run the first entry only',
  fallback: 'fallback: try entries in order until one succeeds',
  fanout: 'fanout: run all entries in parallel — a Merge node combines their outputs',
}

export interface ModelSetEditorProps {
  id: string
  catalog: UseAsyncResult<StudioCatalog>
}

export function ModelSetEditor({ id, catalog }: ModelSetEditorProps) {
  const defQ = useAsync((signal) => getEditableDef('model_set', id, signal), [id])
  const def = defQ.data

  const draft = useDirtyDraft<ModelSetBody>(def?.draft ?? EMPTY_BODY, {
    autosave: def ? (body) => putDraft('model_set', def.id, body) : undefined,
    onSaved: defQ.reload,
  })

  /* Capability filters are UI-only (ModelSetBody has no field for them). */
  const [requireVision, setRequireVision] = useState(false)
  const [requireTools, setRequireTools] = useState(false)

  /* VersionBar persists the draft itself (save/publish/restore). Once the
   * server confirms a draft identical to what's on screen, sync the hook's
   * saved snapshot so the bar stops reporting unsaved changes. Skipped while
   * the user keeps typing (JSON differs → autosave stays in charge). */
  const serverDraft = def?.draft ?? null
  useEffect(() => {
    if (!serverDraft) return
    if (JSON.stringify(draft.draft) === JSON.stringify(serverDraft)) draft.reset(serverDraft)
  }, [serverDraft, draft.draft, draft.reset])

  if (!def) {
    if (defQ.error) {
      return (
        <div className="px-6 py-6">
          <ErrorState error={defQ.error} onRetry={defQ.reload} />
        </div>
      )
    }
    return (
      <div className="mx-auto w-full max-w-2xl space-y-3 px-6 py-5" aria-busy="true">
        <Skeleton className="h-7 w-1/3" />
        <Skeleton className="h-24" />
        <Skeleton className="h-24" />
        <Skeleton className="h-48" />
      </div>
    )
  }

  return (
    <div className="flex flex-col">
      <VersionBar
        kind="model_set"
        def={def}
        draft={draft.draft}
        dirty={draft.dirty}
        onDraftLoaded={(body) => draft.reset((body as ModelSetBody | null) ?? EMPTY_BODY)}
        onSaved={defQ.reload}
        publishBlocked={draft.draft.entries.length === 0 ? 'Add at least one model to publish' : null}
        className="sticky top-0 z-20"
      />

      <div className="mx-auto w-full max-w-2xl px-6 py-4">
        <PageHeader
          back={{ to: '/studio/model-sets', label: 'Model sets' }}
          title={def.name}
          description={def.description === '' ? undefined : def.description}
          className="mb-4"
        />

        {draft.saveState === 'error' && (
          <Callout tone="danger" title="Autosave failed" className="mb-3">
            Your latest changes are not saved yet — edit again or press “Save draft” to retry.
          </Callout>
        )}

        <div className="space-y-3">
          <IdentityCard kind="model_set" def={def} slugHint="Referenced by agents, Agent nodes and exports." onSaved={defQ.reload} />

          <Card>
            <CardHeader title="Strategy" description="How the set behaves when an agent needs a model." />
            <CardBody className="space-y-2">
              <Segmented
                options={[
                  { value: 'single', label: 'Single' },
                  { value: 'fallback', label: 'Fallback' },
                  { value: 'fanout', label: 'Fanout' },
                ]}
                value={draft.draft.strategy}
                onChange={(strategy) => draft.setDraft((prev) => ({ ...prev, strategy }))}
              />
              <p className="text-xs text-ink-3">{STRATEGY_HINTS[draft.draft.strategy]}</p>
            </CardBody>
          </Card>

          <Card>
            <CardHeader title="Models" description="Ordered entries with per-entry parameters." />
            <CardBody className="space-y-3">
              <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
                <Checkbox
                  label="Requires vision"
                  checked={requireVision}
                  onChange={(e) => setRequireVision(e.target.checked)}
                />
                <Checkbox
                  label="Requires tool use"
                  checked={requireTools}
                  onChange={(e) => setRequireTools(e.target.checked)}
                />
                {catalog.loading && !catalog.data && <span className="text-2xs text-ink-3">Loading catalog…</span>}
              </div>
              <p className="text-2xs text-ink-3">Only models with these capabilities are offered below.</p>

              {catalog.error != null && !catalog.data && (
                <Callout tone="danger" title="Failed to load the model catalog">
                  <div className="mt-1.5 flex items-center gap-2.5">
                    <span className="min-w-0 break-words">
                      {catalog.error instanceof Error ? catalog.error.message : 'Something went wrong.'}
                    </span>
                    <Button size="sm" variant="secondary" onClick={catalog.reload} className="shrink-0">
                      Retry
                    </Button>
                  </div>
                </Callout>
              )}

              <EntryList
                entries={draft.draft.entries}
                models={catalog.data?.models ?? []}
                requireVision={requireVision}
                requireTools={requireTools}
                catalogReady={catalog.data != null}
                onChange={(entries) => draft.setDraft((prev) => ({ ...prev, entries }))}
              />

              {draft.draft.strategy === 'fanout' && (
                <Callout tone="info">All entries run in parallel — a Merge node combines their outputs.</Callout>
              )}
            </CardBody>
          </Card>
        </div>
      </div>
    </div>
  )
}
