/**
 * Pipelines — studio section (plan.critique-engine.md §4.5).
 *
 * `?id=<defId>` switches between the list page and the node-graph editor;
 * the back action clears the param. New pipelines start either from the
 * built-in design loop template (Input → Generator → Render → critics →
 * Merge → Gate → Output, loop back to Generator) or as an empty graph.
 */
import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { Workflow } from 'lucide-react'
import { Button, Field, Input, Segmented, Spinner, Textarea } from '../../../ui'
import { Callout } from '../../../ui/Display'
import { ErrorState, ListPage, Modal, relativeTime, isBackendMissing } from '../../../components'
import { createDef, getCatalog, getDef, listDefs, type StudioDef } from '../../../api/studio'
import { useAsync } from '../../../hooks'
import { designLoopTemplate, emptyPipelineBody } from './flow'
import PipelineEditor from './PipelineEditor'

export default function PipelinesPage() {
  const [params, setParams] = useSearchParams()
  const id = params.get('id')

  if (id !== null && id !== '') {
    return <PipelineEditorPage id={id} onBack={() => setParams({})} />
  }
  return <PipelinesList onOpen={(def) => setParams({ id: def.id })} />
}

/* ---------- list ---------- */

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
}

function PipelinesList({ onOpen }: { onOpen: (def: StudioDef<'pipeline'>) => void }) {
  const list = useAsync((signal) => listDefs('pipeline', undefined, signal), [])
  // Catalog + agent defs power the design loop template (node types/ports + agent preselect).
  const templateSources = useAsync(
    (signal) =>
      Promise.all([
        getCatalog(signal),
        listDefs('agent', undefined, signal)
          .then((r) => r.items)
          .catch(() => [] as StudioDef<'agent'>[]),
      ]),
    [],
  )
  const template = templateSources.data ? designLoopTemplate(templateSources.data[0], templateSources.data[1]) : null

  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [useTemplate, setUseTemplate] = useState<'template' | 'blank'>('template')

  const slugValid = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(slug)
  const canCreate = name.trim() !== '' && slugValid

  const openCreate = () => {
    setSlug('')
    setName('')
    setDescription('')
    setSlugTouched(false)
    setCreateError(null)
    setUseTemplate('template')
    setCreateOpen(true)
  }

  const submit = async (): Promise<void> => {
    if (!canCreate || creating) return
    setCreating(true)
    setCreateError(null)
    try {
      const def = await createDef('pipeline', {
        slug,
        name: name.trim(),
        description: description.trim(),
        draft: useTemplate === 'template' && template ? template : emptyPipelineBody(),
      })
      setCreateOpen(false)
      onOpen(def)
    } catch (err) {
      setCreateError(err instanceof Error ? err.message : 'Failed to create pipeline')
    } finally {
      setCreating(false)
    }
  }

  if (list.error && list.data == null) {
    if (isBackendMissing(list.error)) {
      return (
        <div className="p-5">
          <Callout tone="warning" title="The studio backend isn't available yet">
            <p className="mt-1 text-sm">
              Pipelines can't be loaded or saved yet — the studio API hasn't been built. The chat
              will use them as soon as it lands.
            </p>
            {list.error instanceof Error && <p className="mt-1 font-mono text-2xs text-ink-3">{list.error.message}</p>}
          </Callout>
        </div>
      )
    }
    return (
      <div className="p-5">
        <ErrorState error={list.error} onRetry={list.reload} />
      </div>
    )
  }

  return (
    <div className="p-5">
      <ListPage<StudioDef<'pipeline'>>
        title="Pipelines"
        description="A pipeline decides when each agent runs, what it receives and what happens with its output — e.g. generate → render → critique → regenerate while the score is low. The design chat runs the published pipeline."
        newButton={{ label: 'New pipeline', onClick: openCreate }}
        searchPlaceholder="Search pipelines…"
        rows={list.data?.items ?? null}
        loading={list.loading}
        onOpen={onOpen}
        emptyState={{ icon: Workflow, title: 'No pipelines yet', description: 'Create one from the design loop template to get started.' }}
        columns={[
          { key: 'name', header: 'Name', render: (r) => <span className="font-medium text-ink">{r.name}</span> },
          { key: 'slug', header: 'Slug', width: 'w-40', render: (r) => <span className="font-mono text-2xs text-ink-2">{r.slug}</span> },
          { key: 'nodes', header: 'Nodes', width: 'w-20', render: (r) => String(r.draft?.nodes.length ?? 0) },
          { key: 'latest_version', header: 'Version', width: 'w-24', render: (r) => (r.latest_version == null ? '—' : `v${r.latest_version}`) },
          { key: 'draft_updated_at', header: 'Updated', width: 'w-32', render: (r) => relativeTime(r.draft_updated_at) },
        ]}
      />

      <Modal
        open={createOpen}
        onClose={creating ? () => {} : () => setCreateOpen(false)}
        size="sm"
        title="New pipeline"
        description={
          template
            ? 'Start from the design loop template (generate → render → critique → gate) or an empty graph.'
            : 'Starts as an empty graph with one Input and one Output node.'
        }
        footer={
          <>
            {createError && <span className="mr-auto min-w-0 truncate text-xs text-danger">{createError}</span>}
            <Button size="sm" variant="secondary" disabled={creating} onClick={() => setCreateOpen(false)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" loading={creating} disabled={!canCreate} onClick={() => void submit()}>
              Create pipeline
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-3">
          {template && (
            <Field label="Starting point">
              <Segmented
                options={[
                  { value: 'template', label: 'Design loop' },
                  { value: 'blank', label: 'Empty graph' },
                ]}
                value={useTemplate}
                onChange={setUseTemplate}
              />
            </Field>
          )}
          <Field label="Name">
            <Input
              value={name}
              autoFocus
              placeholder="Component critique"
              disabled={creating}
              onChange={(e) => {
                setName(e.target.value)
                if (!slugTouched) setSlug(slugify(e.target.value))
              }}
            />
          </Field>
          <Field label="Slug" error={slug !== '' && !slugValid ? 'Lowercase letters, digits and dashes only.' : undefined} hint="Used in URLs and exports.">
            <Input
              value={slug}
              className="font-mono text-xs"
              placeholder="component-critique"
              disabled={creating}
              onChange={(e) => {
                setSlug(e.target.value.toLowerCase())
                setSlugTouched(true)
              }}
            />
          </Field>
          <Field label="Description" optional>
            <Textarea value={description} rows={2} placeholder="What does this pipeline produce?" disabled={creating} onChange={(e) => setDescription(e.target.value)} />
          </Field>
        </div>
      </Modal>
    </div>
  )
}

/* ---------- editor entry ---------- */

/**
 * Loads the definition, the catalog and the agent list, then renders the
 * editor. The catalog is authoritative for node types/ports; the agent list
 * feeds the Agent-node picker (the catalog carries no agent refs).
 */
function PipelineEditorPage({ id, onBack }: { id: string; onBack: () => void }) {
  const load = useAsync(
    async (signal) => {
      const [def, catalog, agents] = await Promise.all([
        getDef('pipeline', id, signal),
        getCatalog(signal),
        listDefs('agent', undefined, signal)
          .then((r) => r.items)
          .catch(() => [] as StudioDef<'agent'>[]),
      ])
      return { def, catalog, agents }
    },
    [id],
  )

  if (load.error && load.data == null) {
    if (isBackendMissing(load.error)) {
      return (
        <div className="grid h-full place-items-center p-6">
          <Callout tone="warning" title="The studio backend isn't available yet" className="max-w-lg">
            <p className="mt-1 text-sm">Pipelines can't be loaded or saved yet — the studio API hasn't been built.</p>
            {load.error instanceof Error && <p className="mt-1 font-mono text-2xs text-ink-3">{load.error.message}</p>}
          </Callout>
        </div>
      )
    }
    return (
      <div className="grid h-full place-items-center p-6">
        <ErrorState error={load.error} onRetry={load.reload} />
      </div>
    )
  }
  if (load.data == null) {
    return (
      <div className="grid h-full place-items-center">
        <Spinner className="text-ink-3" />
      </div>
    )
  }
  return (
    <PipelineEditor
      key={load.data.def.id}
      def={load.data.def}
      catalog={load.data.catalog}
      agents={load.data.agents}
      onBack={onBack}
      onDefSaved={load.reload}
    />
  )
}
