/**
 * "New run" modal (plan §5 Runs UI): pick a published pipeline (latest
 * version), toggle test-run, fill the pipeline's Input node form, then
 * `startRun` and navigate to the run detail.
 *
 * Uploads don't exist yet, so `image` fields take an artifact id (submitted as
 * `{ artifact_id }`).
 */
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { Workflow } from 'lucide-react'
import { Button } from '../../ui/Button'
import { Field, Input, Switch, Textarea } from '../../ui/Form'
import { Select } from '../../ui/Select'
import { Badge, Callout, EmptyState, Skeleton } from '../../ui/Display'
import { Modal } from '../../components/Modal'
import { InlineSpinner } from '../../components/Errors'
import { useAsync } from '../../hooks'
import { startRun, type RunInputValue } from '../../api/runs'
import { getDef, getVersion, listDefs, type PipelineBody } from '../../api/studio'

interface RunInputField {
  name: string
  label?: string
  type?: string
  required: boolean
  description?: string
  default?: string
}

/** Defensively reads the Input node's `config.fields` from a pipeline body. */
export function parseInputFields(body: PipelineBody | null | undefined): RunInputField[] {
  const inputNode = body?.nodes.find((n) => n.type === 'input')
  const raw = inputNode?.config?.['fields']
  if (!Array.isArray(raw)) return []
  return raw.flatMap((item) => {
    if (item == null || typeof item !== 'object') return []
    const o = item as Record<string, unknown>
    if (typeof o['name'] !== 'string' || !o['name']) return []
    return [
      {
        name: o['name'],
        label: typeof o['label'] === 'string' ? o['label'] : undefined,
        type: typeof o['type'] === 'string' ? o['type'] : undefined,
        required: o['required'] === true,
        description: typeof o['description'] === 'string' ? o['description'] : undefined,
        default: typeof o['default'] === 'string' ? o['default'] : undefined,
      },
    ]
  })
}

export function NewRunModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const defsQ = useAsync(
    (signal) => (open ? listDefs('pipeline', { limit: 100 }, signal) : Promise.resolve({ items: [] })),
    [open],
  )

  const [pipelineId, setPipelineId] = useState<string | null>(null)
  const [isTest, setIsTest] = useState(false)
  const [values, setValues] = useState<Record<string, string>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<unknown>(null)

  // Fresh form every time the modal opens.
  useEffect(() => {
    if (open) {
      setPipelineId(null)
      setIsTest(false)
      setValues({})
      setErrors({})
      setSubmitError(null)
    }
  }, [open])

  const published = useMemo(
    () => (defsQ.data?.items ?? []).filter((d) => d.latest_version != null),
    [defsQ.data],
  )

  // Pinned latest-version body (falls back to the draft body when unavailable).
  const bodyQ = useAsync(
    async (signal) => {
      if (!open || !pipelineId) return null
      const def = defsQ.data?.items.find((d) => d.id === pipelineId)
      const version = def?.latest_version ?? null
      if (version != null) {
        const ver = await getVersion('pipeline', pipelineId, version, signal).catch(() => null)
        if (ver) return { version, body: ver.body }
      }
      const d = await getDef('pipeline', pipelineId, signal).catch(() => null)
      return d?.draft ? { version: version ?? 0, body: d.draft } : null
    },
    [open, pipelineId, defsQ.data],
  )

  const fields = useMemo(() => parseInputFields(bodyQ.data?.body), [bodyQ.data])
  const options = published.map((d) => ({
    value: d.id,
    label: d.name,
    hint: `v${d.latest_version}`,
  }))

  const submit = async (): Promise<void> => {
    const fieldErrors: Record<string, string> = {}
    for (const f of fields) {
      if (f.required && !(values[f.name] ?? '').trim()) fieldErrors[f.name] = 'Required'
    }
    setErrors(fieldErrors)
    if (Object.keys(fieldErrors).length > 0) return

    const input: Record<string, RunInputValue> = {}
    for (const f of fields) {
      const v = (values[f.name] ?? '').trim()
      if (!v) continue
      input[f.name] = f.type === 'image' ? { artifact_id: v } : v
    }

    setSubmitting(true)
    setSubmitError(null)
    try {
      const res = await startRun({ is_test: isTest || undefined, input })
      onClose()
      navigate(`/runs/${res.id}`)
    } catch (err: unknown) {
      setSubmitError(err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={submitting ? () => undefined : onClose}
      title="New run"
      description="Start a pipeline run on the latest published version."
      size="md"
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={submitting} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={submitting}
            disabled={pipelineId == null || bodyQ.loading || bodyQ.error != null}
            onClick={() => void submit()}
          >
            Start run
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {defsQ.loading ? (
          <Skeleton className="h-7 w-full" />
        ) : defsQ.error ? (
          <Callout tone="danger" title="Couldn't load pipelines">
            {(defsQ.error as Error).message}
          </Callout>
        ) : published.length === 0 ? (
          <EmptyState
            icon={Workflow}
            title="No published pipelines"
            description="Publish a pipeline in the Studio first — runs always pin a published version."
          />
        ) : (
          <>
            <Field label="Pipeline">
              <Select
                options={options}
                value={pipelineId}
                onChange={setPipelineId}
                placeholder="Choose a pipeline…"
                ariaLabel="Pipeline"
              />
            </Field>

            <div className="flex items-center justify-between rounded-lg border border-line bg-app px-3 py-2">
              <div>
                <p className="text-base font-medium text-ink">Test run</p>
                <p className="text-2xs text-ink-3">Marked as a test — not counted like production runs.</p>
              </div>
              <Switch checked={isTest} onChange={setIsTest} aria-label="Test run" />
            </div>

            {pipelineId != null && bodyQ.loading && <InlineSpinner label="Loading pipeline…" />}

            {pipelineId != null && !bodyQ.loading && !bodyQ.data && (
              <Callout tone="warning" title="Pipeline body unavailable">
                Couldn't load this pipeline's definition, so its input form can't be shown.
              </Callout>
            )}

            {bodyQ.data && fields.length === 0 && (
              <p className="text-xs text-ink-3">
                This pipeline has no Input fields — it will run with an empty input.
              </p>
            )}

            {fields.map((f) => {
              const type = (f.type ?? 'text').toLowerCase()
              const value = values[f.name] ?? f.default ?? ''
              const isImage = type === 'image'
              return (
                <Field
                  key={f.name}
                  label={f.label || f.name}
                  hint={isImage ? 'Paste an artifact id — uploads aren\'t supported yet.' : f.description}
                  error={errors[f.name]}
                  optional={!f.required}
                >
                  {type === 'textarea' ? (
                    <Textarea
                      rows={4}
                      value={value}
                      onChange={(e) => setValues((prev) => ({ ...prev, [f.name]: e.target.value }))}
                      placeholder={f.description}
                    />
                  ) : (
                    <Input
                      value={value}
                      onChange={(e) => setValues((prev) => ({ ...prev, [f.name]: e.target.value }))}
                      placeholder={isImage ? 'artifact id' : undefined}
                      className={isImage ? 'font-mono' : undefined}
                    />
                  )}
                </Field>
              )
            })}

            {submitError != null && (
              <Callout tone="danger" title="Couldn't start the run">
                {submitError instanceof Error ? submitError.message : 'Something went wrong.'}
              </Callout>
            )}

            {bodyQ.data?.version ? (
              <p className="flex items-center gap-1.5 text-2xs text-ink-3">
                Runs pin the pipeline version they start on.
                <Badge tone="neutral">v{bodyQ.data.version}</Badge>
              </p>
            ) : null}
          </>
        )}
      </div>
    </Modal>
  )
}
