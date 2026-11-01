/**
 * Rules list (plan §4.6) — hairline table of rule-set definitions with a
 * slug/name/description create modal; rows open the editor via `?id=`.
 */
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { ShieldCheck } from 'lucide-react'
import { Button, Field, Input, Textarea } from '../../../ui'
import { ErrorState, ListPage, Modal, relativeTime } from '../../../components'
import { useAsync } from '../../../hooks'
import { createDef, listDefs, type StudioDef } from '../../../api/studio'
import { emptyRuleBody } from './body'

const BASE = '/studio/rules'
const SLUG_RE = /^[a-z0-9-]+$/

/** "My Cool Rules" → "my-cool-rules" (prefills the slug field). */
function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

export default function RulesList() {
  const navigate = useNavigate()
  const list = useAsync((signal) => listDefs<'rule'>('rule', undefined, signal), [])
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="p-6">
      {list.error && !list.data ? (
        <ErrorState error={list.error} onRetry={list.reload} />
      ) : (
        <ListPage<StudioDef<'rule'>>
          title="Rules"
          description="Grouped critique rules attached to agents — text, severity and an optional validator code."
          newButton={{ label: 'New rule set', onClick: () => setCreateOpen(true) }}
          searchPlaceholder="Search rule sets…"
          rows={list.data?.items ?? null}
          loading={list.loading && !list.data}
          onOpen={(def) => navigate(`${BASE}?id=${encodeURIComponent(def.id)}`)}
          emptyState={{
            icon: ShieldCheck,
            title: 'No rule sets yet',
            description: 'Create a rule set to give agents shared critique rules.',
          }}
          columns={[
            { key: 'name', header: 'Name', render: (def) => <span className="font-medium text-ink">{def.name}</span> },
            {
              key: 'slug',
              header: 'Slug',
              width: 'w-44',
              render: (def) => <span className="font-mono text-xs text-ink-2">{def.slug}</span>,
            },
            {
              key: 'rules',
              header: 'Rules',
              width: 'w-20',
              render: (def) => `${def.draft?.groups.reduce((n, g) => n + g.rules.length, 0) ?? 0} rules`,
            },
            {
              key: 'latest_version',
              header: 'Version',
              width: 'w-24',
              render: (def) => (def.latest_version != null ? `v${def.latest_version}` : '—'),
            },
            {
              key: 'updated',
              header: 'Updated',
              width: 'w-28',
              render: (def) => relativeTime(def.draft_updated_at),
            },
          ]}
        />
      )}

      {createOpen && (
        <CreateRuleModal
          onClose={() => setCreateOpen(false)}
          onCreated={(def) => navigate(`${BASE}?id=${encodeURIComponent(def.id)}`)}
        />
      )}
    </div>
  )
}

function CreateRuleModal({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (def: StudioDef<'rule'>) => void
}) {
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [description, setDescription] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const trimmedSlug = slug.trim()
  const slugError =
    trimmedSlug.length > 0 && !SLUG_RE.test(trimmedSlug) ? 'Use lowercase letters, digits and dashes.' : null
  const canCreate = name.trim().length > 0 && trimmedSlug.length > 0 && slugError == null && !creating

  const submit = async (): Promise<void> => {
    if (!canCreate) return
    setCreating(true)
    setError(null)
    try {
      const def = await createDef('rule', {
        slug: trimmedSlug,
        name: name.trim(),
        description: description.trim() || undefined,
        draft: emptyRuleBody(),
      })
      onCreated(def)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create the rule set.')
      setCreating(false)
    }
  }

  return (
    <Modal
      open
      size="sm"
      title="New rule set"
      description="Grouped rules are appended to the agent prompt as a “Rules” section."
      onClose={creating ? () => {} : onClose}
      footer={
        <>
          {error && <span className="mr-auto min-w-0 truncate text-xs text-danger">{error}</span>}
          <Button size="sm" variant="secondary" disabled={creating} onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" variant="primary" loading={creating} disabled={!canCreate} onClick={() => void submit()}>
            Create rule set
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label="Name">
          <Input
            autoFocus
            value={name}
            placeholder="Component format"
            onChange={(e) => {
              setName(e.target.value)
              if (!slugTouched) setSlug(slugify(e.target.value))
            }}
          />
        </Field>
        <Field label="Slug" error={slugError ?? undefined} hint="Lowercase letters, digits and dashes.">
          <Input
            className="font-mono text-xs"
            value={slug}
            placeholder="component-format"
            onChange={(e) => {
              setSlugTouched(true)
              setSlug(e.target.value)
            }}
          />
        </Field>
        <Field label="Description" optional>
          <Textarea
            rows={2}
            value={description}
            placeholder="What are these rules for?"
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
      </div>
    </Modal>
  )
}
