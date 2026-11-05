/**
 * Style guides in the chat UI: the list hook (polls while one is generating),
 * the pill row of the chats page (create + one pill per guide → a new chat
 * with it), the create modal (sample images → vision generation), the detail
 * modal (samples, markdown, edit / regenerate / delete) and the composer's
 * picker (new chats) / locked pill (a chat's guide never changes).
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ChevronUp, ImagePlus, Lock, MoreHorizontal, Palette, Plus, RotateCw, Trash2, X } from 'lucide-react'
import { Button, Field, Input, Spinner, Textarea, cx, useDismiss } from '../../ui'
import { Callout } from '../../ui/Display'
import { ConfirmDialog, Markdown, Modal } from '../../components'
import {
  MAX_STYLEGUIDE_IMAGES,
  MAX_STYLEGUIDE_IMAGE_BYTES,
  createStyleguide,
  deleteStyleguide,
  listStyleguides,
  regenerateStyleguide,
  styleguideImageURL,
  updateStyleguide,
  type Styleguide,
} from '../../api/styleguides'

/** How often the list refreshes while a guide is being generated. */
const POLL_MS = 3000

const errText = (err: unknown, fallback: string): string => (err instanceof Error && err.message ? err.message : fallback)

/* ---------- list ---------- */

export interface StyleguidesValue {
  guides: Styleguide[]
  byId: Map<string, Styleguide>
  loading: boolean
  error: unknown
  reload: () => void
  /** Puts a fresh copy (create / edit) into the list before the next reload. */
  upsert: (sg: Styleguide) => void
  remove: (id: string) => void
}

export function useStyleguides(): StyleguidesValue {
  const [guides, setGuides] = useState<Styleguide[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    const ctrl = new AbortController()
    listStyleguides(ctrl.signal)
      .then((items) => {
        setGuides(items)
        setError(null)
      })
      .catch((err: unknown) => {
        if (!ctrl.signal.aborted) setError(err)
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    return () => ctrl.abort()
  }, [tick])

  const generating = guides.some((g) => g.status === 'generating')
  useEffect(() => {
    if (!generating) return
    const t = window.setTimeout(reload, POLL_MS)
    return () => window.clearTimeout(t)
  }, [generating, guides, reload])

  const upsert = useCallback((sg: Styleguide) => {
    setGuides((prev) => (prev.some((g) => g.id === sg.id) ? prev.map((g) => (g.id === sg.id ? sg : g)) : [sg, ...prev]))
  }, [])
  const remove = useCallback((id: string) => setGuides((prev) => prev.filter((g) => g.id !== id)), [])
  const byId = useMemo(() => new Map(guides.map((g) => [g.id, g])), [guides])
  return { guides, byId, loading, error, reload, upsert, remove }
}

/* ---------- chats page: pills ---------- */

const PILL =
  'inline-flex h-8 shrink-0 cursor-pointer items-center gap-1.5 rounded-full border text-sm outline-none transition duration-100 focus-visible:ring-2 focus-visible:ring-accent/40 active:scale-[0.98]'

/**
 * The chats page row: "+ Style guide" (create), then one pill per guide.
 * Tapping a ready guide starts a new chat with it; its "…" (or tapping a
 * generating / failed one) opens the details.
 */
export function StyleguidePills({
  guides,
  onCreate,
  onUse,
  onOpen,
}: {
  guides: Styleguide[]
  onCreate: () => void
  onUse: (id: string) => void
  onOpen: (id: string) => void
}) {
  return (
    <div className="-mx-4 flex gap-2 overflow-x-auto px-4 pb-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
      <button
        type="button"
        onClick={onCreate}
        className={cx(PILL, 'border-dashed border-line-strong bg-surface px-3 text-ink-2 hover:border-accent/60 hover:text-ink')}
      >
        <Plus size={14} />
        Style guide
      </button>
      {guides.map((g) => (
        <span
          key={g.id}
          className={cx(
            PILL,
            'cursor-default pr-0.5 pl-0',
            g.status === 'failed' ? 'border-danger/40 bg-danger-soft' : 'border-line bg-surface hover:border-line-strong',
          )}
        >
          <button
            type="button"
            onClick={() => (g.status === 'ready' ? onUse(g.id) : onOpen(g.id))}
            title={g.status === 'ready' ? `New chat with the “${g.name}” style guide` : g.status === 'failed' ? g.error : 'Generating…'}
            className="flex h-full max-w-56 cursor-pointer items-center gap-1.5 rounded-l-full pl-3 outline-none"
          >
            {g.status === 'generating' ? (
              <Spinner size={13} />
            ) : (
              <Palette size={14} className={g.status === 'failed' ? 'text-danger' : 'text-accent'} />
            )}
            <span className={cx('truncate', g.status === 'failed' ? 'text-danger' : 'text-ink')}>
              {g.status === 'generating' && !g.name ? 'Generating…' : g.name}
            </span>
          </button>
          <button
            type="button"
            aria-label={`Details of ${g.name}`}
            onClick={() => onOpen(g.id)}
            className="grid size-7 cursor-pointer place-items-center rounded-full text-ink-3 outline-none hover:bg-app hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40"
          >
            <MoreHorizontal size={14} />
          </button>
        </span>
      ))}
    </div>
  )
}

/* ---------- create ---------- */

interface Picked {
  file: File
  url: string
}

export function CreateStyleguideModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: (sg: Styleguide) => void
}) {
  const [picked, setPicked] = useState<Picked[]>([])
  const [name, setName] = useState('')
  const [notes, setNotes] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  // revoke preview URLs when they leave the list / on unmount
  const urls = useRef<string[]>([])
  useEffect(() => {
    const live = new Set(picked.map((p) => p.url))
    for (const u of urls.current) if (!live.has(u)) URL.revokeObjectURL(u)
    urls.current = [...live]
  }, [picked])
  useEffect(() => () => urls.current.forEach((u) => URL.revokeObjectURL(u)), [])

  const reset = (): void => {
    setPicked([])
    setName('')
    setNotes('')
    setError(null)
  }
  const close = (): void => {
    if (busy) return
    reset()
    onClose()
  }

  const add = (files: FileList | File[]): void => {
    const list = [...files].filter((f) => f.type.startsWith('image/'))
    const tooBig = list.filter((f) => f.size > MAX_STYLEGUIDE_IMAGE_BYTES)
    const ok = list.filter((f) => f.size <= MAX_STYLEGUIDE_IMAGE_BYTES)
    setPicked((prev) => {
      const room = MAX_STYLEGUIDE_IMAGES - prev.length
      if (ok.length > room) setError(`Up to ${MAX_STYLEGUIDE_IMAGES} images.`)
      else if (tooBig.length > 0) setError(`${tooBig.map((f) => f.name).join(', ')}: larger than 8 MB.`)
      else if (list.length === 0 && files.length > 0) setError('Only images (PNG, JPEG, WebP, GIF).')
      else setError(null)
      return [...prev, ...ok.slice(0, Math.max(0, room)).map((file) => ({ file, url: URL.createObjectURL(file) }))]
    })
  }

  const submit = async (): Promise<void> => {
    if (picked.length === 0 || busy) return
    setBusy(true)
    setError(null)
    try {
      const sg = await createStyleguide(
        picked.map((p) => p.file),
        name,
        notes,
      )
      reset()
      onCreated(sg)
    } catch (err: unknown) {
      setError(errText(err, "Couldn't upload the images."))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={close}
      size="lg"
      title="New style guide"
      description="Upload screenshots or mockups of the look you want. A vision model extracts colors, type, spacing and component styles into a guide your chats can follow."
      footer={
        <>
          <Button variant="secondary" onClick={close} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} loading={busy} disabled={picked.length === 0}>
            Generate
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div
          onDragOver={(e) => {
            e.preventDefault()
            setDragging(true)
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault()
            setDragging(false)
            add(e.dataTransfer.files)
          }}
          className={cx(
            'flex flex-wrap gap-2 rounded-xl border border-dashed p-2 transition-colors',
            dragging ? 'border-accent bg-accent-soft' : 'border-line-strong bg-app',
          )}
        >
          {picked.map((p, i) => (
            <div key={p.url} className="group relative size-24 overflow-hidden rounded-lg border border-line bg-surface">
              <img src={p.url} alt={p.file.name} className="size-full object-cover" />
              <button
                type="button"
                aria-label={`Remove ${p.file.name}`}
                onClick={() => setPicked((prev) => prev.filter((_, j) => j !== i))}
                className="absolute top-1 right-1 grid size-6 cursor-pointer place-items-center rounded-full bg-ink/70 text-white outline-none hover:bg-ink focus-visible:ring-2 focus-visible:ring-white/60"
              >
                <X size={12} />
              </button>
            </div>
          ))}
          {picked.length < MAX_STYLEGUIDE_IMAGES && (
            <button
              type="button"
              onClick={() => inputRef.current?.click()}
              className={cx(
                'flex cursor-pointer flex-col items-center justify-center gap-1 rounded-lg text-xs text-ink-2 outline-none hover:text-ink focus-visible:ring-2 focus-visible:ring-accent/40',
                picked.length === 0 ? 'h-32 w-full' : 'size-24 border border-line bg-surface',
              )}
            >
              <ImagePlus size={picked.length === 0 ? 22 : 18} />
              {picked.length === 0 ? `Drop or choose up to ${MAX_STYLEGUIDE_IMAGES} images` : 'Add'}
            </button>
          )}
          <input
            ref={inputRef}
            type="file"
            accept="image/png,image/jpeg,image/webp,image/gif"
            multiple
            hidden
            onChange={(e) => {
              if (e.target.files) add(e.target.files)
              e.target.value = ''
            }}
          />
        </div>
        <Field label="Name" optional hint="Leave empty and the model names it.">
          <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={200} placeholder="e.g. Acme marketing" />
        </Field>
        <Field label="Notes" optional hint="What to keep, ignore or emphasise.">
          <Textarea
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            maxLength={4000}
            rows={3}
            placeholder="e.g. Use the orange only as an accent; ignore the photos."
          />
        </Field>
        {error && <p className="text-xs text-danger">{error}</p>}
      </div>
    </Modal>
  )
}

/* ---------- details ---------- */

function SampleImage({ id, n }: { id: string; n: number }) {
  const [url, setUrl] = useState<string | null>(null)
  useEffect(() => {
    const ctrl = new AbortController()
    let made: string | null = null
    styleguideImageURL(id, n, ctrl.signal)
      .then((u) => {
        made = u
        setUrl(u)
      })
      .catch(() => {})
    return () => {
      ctrl.abort()
      if (made) URL.revokeObjectURL(made)
    }
  }, [id, n])
  return (
    <a
      href={url ?? undefined}
      target="_blank"
      rel="noreferrer"
      className="block size-24 shrink-0 overflow-hidden rounded-lg border border-line bg-app"
    >
      {url ? <img src={url} alt={`Sample ${n + 1}`} className="size-full object-cover" /> : <span className="block size-full animate-pulse" />}
    </a>
  )
}

/** Details of one guide (live: `guide` comes from the polled list). */
export function StyleguideModal({
  guide,
  onClose,
  onChanged,
  onDeleted,
  onUse,
}: {
  guide: Styleguide | null
  onClose: () => void
  onChanged: (sg: Styleguide) => void
  onDeleted: (id: string) => void
  onUse: (id: string) => void
}) {
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [markdown, setMarkdown] = useState('')
  const [busy, setBusy] = useState<'save' | 'regenerate' | 'delete' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const id = guide?.id
  useEffect(() => {
    setEditing(false)
    setError(null)
  }, [id])

  if (!guide) return null

  const act = async (kind: 'save' | 'regenerate' | 'delete'): Promise<void> => {
    setBusy(kind)
    setError(null)
    try {
      if (kind === 'save') {
        onChanged(await updateStyleguide(guide.id, { name, markdown }))
        setEditing(false)
      } else if (kind === 'regenerate') {
        onChanged(await regenerateStyleguide(guide.id))
      } else {
        await deleteStyleguide(guide.id)
        setConfirmDelete(false)
        onDeleted(guide.id)
      }
    } catch (err: unknown) {
      setError(errText(err, 'Something went wrong.'))
      setConfirmDelete(false)
    } finally {
      setBusy(null)
    }
  }

  const generating = guide.status === 'generating'
  let footer: ReactNode
  if (editing) {
    footer = (
      <>
        <Button variant="secondary" onClick={() => setEditing(false)} disabled={busy != null}>
          Cancel
        </Button>
        <Button variant="primary" onClick={() => void act('save')} loading={busy === 'save'} disabled={!name.trim() || !markdown.trim()}>
          Save
        </Button>
      </>
    )
  } else {
    footer = (
      <>
        <Button variant="ghost" icon={<Trash2 />} onClick={() => setConfirmDelete(true)} disabled={busy != null || generating} className="mr-auto text-danger">
          Delete
        </Button>
        {guide.image_count > 0 && (
          <Button variant="secondary" icon={<RotateCw />} onClick={() => void act('regenerate')} loading={busy === 'regenerate'} disabled={generating}>
            Regenerate
          </Button>
        )}
        <Button
          variant="secondary"
          onClick={() => {
            setName(guide.name)
            setMarkdown(guide.markdown)
            setEditing(true)
          }}
          disabled={generating}
        >
          {guide.markdown ? 'Edit' : 'Write by hand'}
        </Button>
        {guide.status === 'ready' && (
          <Button variant="primary" onClick={() => onUse(guide.id)}>
            Start a chat
          </Button>
        )}
      </>
    )
  }

  return (
    <>
      <Modal open onClose={busy ? () => {} : onClose} size="xl" title={guide.name} footer={<div className="flex w-full flex-wrap justify-end gap-2">{footer}</div>}>
        <div className="flex flex-col gap-4">
          {guide.image_count > 0 && (
            <div className="flex gap-2 overflow-x-auto">
              {Array.from({ length: guide.image_count }, (_, n) => (
                <SampleImage key={n} id={guide.id} n={n} />
              ))}
            </div>
          )}
          {guide.notes && <p className="text-xs text-ink-2">Notes: {guide.notes}</p>}
          {generating && (
            <Callout tone="info" title="Generating…">
              The model is studying the images. This usually takes a minute or two; you can close this window.
            </Callout>
          )}
          {guide.status === 'failed' && (
            <Callout tone="danger" title="Generation failed">
              {guide.error || 'Unknown error.'} Regenerate to try again, or write the guide by hand.
            </Callout>
          )}
          {error && <p className="text-xs text-danger">{error}</p>}
          {editing ? (
            <>
              <Field label="Name">
                <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={200} />
              </Field>
              <Field label="Markdown" className="w-full">
                <Textarea value={markdown} onChange={(e) => setMarkdown(e.target.value)} rows={18} className="w-full font-mono text-xs" />
              </Field>
            </>
          ) : (
            guide.markdown && <Markdown text={guide.markdown} className="text-sm" />
          )}
        </div>
      </Modal>
      <ConfirmDialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        title="Delete style guide?"
        body="Chats that already use it keep following it; it just won't be offered for new chats."
        confirmLabel="Delete"
        danger
        busy={busy === 'delete'}
        onConfirm={() => void act('delete')}
      />
    </>
  )
}

/* ---------- composer ---------- */

const COMPOSER_PILL =
  'inline-flex h-6 max-w-full items-center gap-1 rounded-full px-2.5 text-xs font-medium outline-none focus-visible:ring-2 focus-visible:ring-accent/40'

/** A chat's style guide, fixed once the chat has started. */
export function LockedStyleguide({ name }: { name: string }) {
  return (
    <span title="This chat's style guide — it can't be changed after the first message" className={cx(COMPOSER_PILL, 'bg-app text-ink-2')}>
      <Palette size={12} className="shrink-0 text-accent" />
      <span className="truncate">{name}</span>
      <Lock size={11} className="shrink-0 text-ink-3" />
    </span>
  )
}

/** Style guide choice of a new chat (opens upwards: the composer sits at the bottom). */
export function StyleguidePicker({
  guides,
  value,
  onChange,
}: {
  guides: Styleguide[]
  value: string
  onChange: (id: string) => void
}) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const close = useCallback(() => setOpen(false), [])
  useDismiss(rootRef, close, open)
  const current = guides.find((g) => g.id === value)
  const label = value ? (current?.name ?? 'Style guide') : 'No style guide'

  const row =
    'flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none hover:bg-app focus-visible:bg-app disabled:cursor-default disabled:opacity-60'
  return (
    <div ref={rootRef} className="relative inline-flex max-w-full">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        title="The style guide this chat follows (fixed after the first message)"
        className={cx(COMPOSER_PILL, 'cursor-pointer', value ? 'bg-accent-soft text-accent-ink hover:bg-accent/15' : 'bg-app text-ink-2 hover:text-ink')}
      >
        <Palette size={12} className="shrink-0" />
        <span className="truncate">{label}</span>
        <ChevronUp size={12} className={cx('shrink-0 transition-transform', !open && 'rotate-180')} />
      </button>
      {open && (
        <div
          role="listbox"
          className="animate-pop absolute bottom-full left-0 z-40 mb-1.5 flex max-h-72 w-64 origin-bottom-left flex-col overflow-y-auto rounded-lg border border-line bg-surface p-1 shadow-pop"
        >
          <button
            type="button"
            role="option"
            aria-selected={!value}
            className={cx(row, !value && 'font-medium text-accent-ink')}
            onClick={() => {
              onChange('')
              close()
            }}
          >
            <span className="size-3.5 shrink-0" />
            No style guide
          </button>
          {guides.map((g) => (
            <button
              key={g.id}
              type="button"
              role="option"
              aria-selected={g.id === value}
              disabled={g.status !== 'ready'}
              className={cx(row, g.id === value && 'font-medium text-accent-ink')}
              onClick={() => {
                onChange(g.id)
                close()
              }}
            >
              {g.status === 'generating' ? <Spinner size={13} /> : <Palette size={14} className={g.status === 'failed' ? 'text-danger' : 'text-accent'} />}
              <span className="min-w-0 flex-1 truncate">{g.name}</span>
              {g.status === 'generating' && <span className="text-2xs text-ink-3">generating</span>}
              {g.status === 'failed' && <span className="text-2xs text-danger">failed</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
