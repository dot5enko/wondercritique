/**
 * Model sets list page (plan §3.4): searchable table of model-set definitions
 * plus the create modal (slug / name / description). Opening a row navigates
 * to the editor via the `?id=` search param.
 */
import { useEffect, useState } from 'react'
import { Layers } from 'lucide-react'
import { useSearchParams } from 'react-router'
import { useAsync } from '../../../hooks'
import { createDef, listDefs } from '../../../api/studio'
import type { ModelSetBody } from '../../../api/studio'
import { ErrorState, ListPage, Modal } from '../../../components'
import { absoluteTime, relativeTime } from '../../../components'
import { Badge, Button, Field, Input, Textarea } from '../../../ui'

const SLUG_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/

const EMPTY_DRAFT: ModelSetBody = { strategy: 'single', entries: [] }

export function ModelSetsList() {
  const [, setSearchParams] = useSearchParams()
  const defs = useAsync((signal) => listDefs('model_set', undefined, signal), [])
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="mx-auto w-full max-w-4xl px-6 py-5">
      {defs.error && !defs.data ? (
        <ErrorState error={defs.error} onRetry={defs.reload} />
      ) : (
        <ListPage
          title="Model sets"
          description="Named, ordered model lists picked from enabled providers — agents and Agent nodes pick one."
          newButton={{ label: 'New model set', onClick: () => setCreateOpen(true) }}
          searchPlaceholder="Search model sets…"
          rows={defs.data?.items ?? null}
          loading={defs.loading && !defs.data}
          onOpen={(def) => setSearchParams({ id: def.id })}
          emptyState={{
            icon: Layers,
            title: 'No model sets yet',
            description: 'Create a named, ordered list of models for your agents.',
          }}
          columns={[
            {
              key: 'name',
              header: 'Name',
              render: (def) => (
                <span className="flex items-center gap-2">
                  <span className="min-w-0 truncate font-medium text-ink">{def.name}</span>
                  {def.slug === 'default' && <Badge tone="accent">default</Badge>}
                </span>
              ),
            },
            {
              key: 'slug',
              header: 'Slug',
              width: 'w-44',
              render: (def) => <span className="truncate font-mono text-xs">{def.slug}</span>,
            },
            {
              key: 'strategy',
              header: 'Strategy',
              width: 'w-24',
              render: (def) => (def.draft ? <Badge>{def.draft.strategy}</Badge> : '—'),
            },
            {
              key: 'models',
              header: 'Models',
              width: 'w-24',
              render: (def) =>
                def.draft ? `${def.draft.entries.length} model${def.draft.entries.length === 1 ? '' : 's'}` : '—',
            },
            {
              key: 'latest_version',
              header: 'Version',
              width: 'w-20',
              render: (def) => (def.latest_version != null ? `v${def.latest_version}` : '—'),
            },
            {
              key: 'draft_updated_at',
              header: 'Updated',
              width: 'w-28',
              render: (def) => (
                <span title={absoluteTime(def.draft_updated_at)}>{relativeTime(def.draft_updated_at)}</span>
              ),
            },
          ]}
        />
      )}
      <NewModelSetModal open={createOpen} onClose={() => setCreateOpen(false)} />
    </div>
  )
}

function NewModelSetModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [, setSearchParams] = useSearchParams()
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  /* Fresh form every time the modal opens. */
  useEffect(() => {
    if (open) {
      setSlug('')
      setName('')
      setDescription('')
      setError(null)
    }
  }, [open])

  const slugError = slug !== '' && !SLUG_RE.test(slug) ? 'Use lowercase letters, numbers and hyphens.' : null
  const canCreate = slug !== '' && name.trim() !== '' && slugError === null

  const create = async (): Promise<void> => {
    if (!canCreate) return
    setCreating(true)
    setError(null)
    try {
      const created = await createDef('model_set', {
        slug,
        name: name.trim(),
        description: description.trim() === '' ? undefined : description.trim(),
        draft: EMPTY_DRAFT,
      })
      onClose()
      setSearchParams({ id: created.id })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create the model set.')
    } finally {
      setCreating(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={creating ? () => {} : onClose}
      size="sm"
      title="New model set"
      description="Agents and Agent nodes pick one of these sets."
      footer={
        <>
          {error && <span className="mr-auto min-w-0 truncate text-xs text-danger">{error}</span>}
          <Button size="sm" variant="secondary" disabled={creating} onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" variant="primary" loading={creating} disabled={!canCreate} onClick={() => void create()}>
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label="Slug" hint="Lowercase letters, numbers and hyphens — e.g. fast-vision." error={slugError ?? undefined}>
          <Input
            autoFocus
            value={slug}
            placeholder="fast-vision"
            className="font-mono"
            disabled={creating}
            onChange={(e) => setSlug(e.target.value.toLowerCase())}
          />
        </Field>
        <Field label="Name">
          <Input
            value={name}
            placeholder="Fast vision set"
            disabled={creating}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label="Description" optional>
          <Textarea
            value={description}
            rows={2}
            placeholder="What is this set for?"
            disabled={creating}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
      </div>
    </Modal>
  )
}
