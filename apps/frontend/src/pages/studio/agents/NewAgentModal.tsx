/**
 * "New agent" dialog: slug/name/description + a sensible default draft
 * (empty prompts, no tools, `critique` output contract). The caller navigates
 * into the editor via onCreated.
 */
import { useEffect, useState } from 'react'
import { Modal } from '../../../components'
import { Button, Field, Input, Textarea } from '../../../ui'
import { createDef, type StudioDef } from '../../../api/studio'
import { SLUG_PATTERN, sensibleDefaultAgentBody, slugify } from './defaults'

export interface NewAgentModalProps {
  open: boolean
  onClose: () => void
  onCreated: (def: StudioDef<'agent'>) => void
}

export function NewAgentModal({ open, onClose, onCreated }: NewAgentModalProps) {
  const [name, setName] = useState('')
  const [slug, setSlug] = useState('')
  const [slugTouched, setSlugTouched] = useState(false)
  const [description, setDescription] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  /* Fresh form every time the dialog opens. */
  useEffect(() => {
    if (open) {
      setName('')
      setSlug('')
      setSlugTouched(false)
      setDescription('')
      setError(null)
    }
  }, [open])

  const submit = async (): Promise<void> => {
    const trimmedName = name.trim()
    const trimmedSlug = slug.trim()
    if (!trimmedName) {
      setError('Name is required.')
      return
    }
    if (!SLUG_PATTERN.test(trimmedSlug)) {
      setError('Slug may only contain lowercase letters, digits and dashes.')
      return
    }
    setCreating(true)
    setError(null)
    try {
      const def = await createDef('agent', {
        slug: trimmedSlug,
        name: trimmedName,
        description: description.trim() || undefined,
        draft: sensibleDefaultAgentBody(),
      })
      onCreated(def)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create the agent.')
    } finally {
      setCreating(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={creating ? () => {} : onClose}
      size="sm"
      title="New agent"
      description="Creates the definition with an empty draft — publish when it's ready."
      footer={
        <>
          {error && <span className="mr-auto min-w-0 truncate text-xs text-danger">{error}</span>}
          <Button size="sm" variant="secondary" disabled={creating} onClick={onClose}>
            Cancel
          </Button>
          <Button size="sm" variant="primary" loading={creating} onClick={() => void submit()}>
            Create agent
          </Button>
        </>
      }
    >
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault()
          if (!creating) void submit()
        }}
      >
        <Field label="Name">
          <Input
            autoFocus
            value={name}
            disabled={creating}
            onChange={(e) => {
              setName(e.target.value)
              if (!slugTouched) setSlug(slugify(e.target.value))
            }}
          />
        </Field>
        <Field label="Slug" hint="Lowercase letters, digits and dashes — used in API paths.">
          <Input
            value={slug}
            disabled={creating}
            className="font-mono text-xs"
            onChange={(e) => {
              setSlugTouched(true)
              setSlug(e.target.value)
            }}
          />
        </Field>
        <Field label="Description" optional>
          <Textarea rows={2} value={description} disabled={creating} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        {/* Implicit-submit target so Enter creates the agent. */}
        <button type="submit" className="hidden" aria-hidden tabIndex={-1} />
      </form>
    </Modal>
  )
}
