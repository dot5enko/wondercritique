/**
 * Identity card for studio editors: editable name + description (saved with an
 * explicit button, independent of the draft/publish flow) and a read-only slug.
 */
import { useEffect, useState } from 'react'
import { updateIdentity } from '../api/studio'
import type { StudioDef, StudioKind } from '../api/studio'
import { Button, Card, CardBody, CardHeader, Field, Input, Textarea } from '../ui'

export interface IdentityCardProps {
  kind: StudioKind
  def: Pick<StudioDef<StudioKind>, 'id' | 'name' | 'slug' | 'description'>
  slugHint?: string
  /** Called after a successful save so the parent can reload the def. */
  onSaved: () => void
  /** Borderless, compact variant. */
  plain?: boolean
}

export function IdentityCard({ kind, def, slugHint, onSaved, plain }: IdentityCardProps) {
  const [name, setName] = useState(def.name)
  const [description, setDescription] = useState(def.description ?? '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Adopt server values (reload / switching defs).
  useEffect(() => {
    setName(def.name)
    setDescription(def.description ?? '')
  }, [def.id, def.name, def.description])

  const dirty = name !== def.name || description !== (def.description ?? '')
  const canSave = dirty && name.trim() !== '' && !saving

  const save = async (): Promise<void> => {
    setSaving(true)
    setError(null)
    try {
      await updateIdentity(kind, def.id, { name: name.trim(), description })
      onSaved()
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card plain={plain}>
      <CardHeader plain={plain} title="Identity" description="Name and description apply immediately when saved — they aren't versioned, so there's nothing to publish. The slug is fixed at creation." />
      <CardBody plain={plain} className="space-y-3">
        <Field label="Name" error={name.trim() === '' ? 'Name is required' : undefined}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description" optional>
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} rows={2} className="resize-none" />
        </Field>
        <Field label="Slug" hint={slugHint}>
          <Input value={def.slug} readOnly className="cursor-default font-mono text-xs" />
        </Field>
        {(dirty || error) && (
          <div className="flex items-center gap-3">
            {dirty && (
              <Button variant="primary" size="sm" loading={saving} onClick={() => void save()} disabled={!canSave}>
                Save identity
              </Button>
            )}
            {error && <span className="text-xs text-danger">{error}</span>}
          </div>
        )}
      </CardBody>
    </Card>
  )
}
