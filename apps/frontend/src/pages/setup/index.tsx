/**
 * First-run setup wizard (admin only, `/setup`).
 *
 * Shown when no LLM provider is configured yet (see `getSetupStatus`):
 *   1. Welcome — what the wizard sets up.
 *   2. Provider — pick OpenRouter or Meta, paste the API key, save, test.
 *   3. Model — pick a Meta-family model, saved into the `default` model set
 *      (single strategy) and published so agents can run.
 *   4. Done — summary + link into the app.
 *
 * Providers and models are limited by the backend to openrouter + meta and
 * the Meta model family; the wizard mirrors that filter client-side so a
 * stale catalog never offers anything else.
 */
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { ArrowLeft, ArrowRight, CheckCircle2, KeyRound, PlugZap, RefreshCw } from 'lucide-react'
import {
  getProviders,
  getSetupStatus,
  mergeProviders,
  testProvider,
  updateProvider,
  type ProviderWithState,
} from '../../api/settings'
import {
  createDef,
  getCatalog,
  getEditableDef,
  listDefs,
  publishDef,
  putDraft,
  type CatalogModel,
  type ModelSetEntry,
} from '../../api/studio'
import { Loadable, PageHeader } from '../../components'
import { useAsync } from '../../hooks'
import { Badge, Button, Callout, Card, CardBody, CardHeader, Field, Input, cx } from '../../ui'
import { AdapterBadge } from '../settings/parts'

type Step = 'welcome' | 'provider' | 'model' | 'done'

const STEPS: { id: Step; label: string }[] = [
  { id: 'welcome', label: 'Welcome' },
  { id: 'provider', label: 'Provider' },
  { id: 'model', label: 'Model' },
  { id: 'done', label: 'Done' },
]

/** Client-side mirror of the backend Meta-family filter (llm.IsAllowedModel). */
export function isMetaFamilyModel(providerId: string, modelId: string): boolean {
  if (providerId === 'meta') return true
  const lower = modelId.toLowerCase()
  return lower.includes('muse') || lower.includes('meta') || lower.includes('llama')
}

/** Preferred default: the seeded muse-spark contributor model, else first. */
function pickDefaultModel(models: CatalogModel[]): CatalogModel | undefined {
  return (
    models.find((m) => m.provider_id === 'meta' && m.model_id === 'muse-spark-1.3-contributor') ??
    models[0]
  )
}

export default function SetupPage() {
  const navigate = useNavigate()
  const providersQ = useAsync(getProviders, [])
  const statusQ = useAsync(getSetupStatus, [])

  const providers = useMemo(
    () => (providersQ.data ? mergeProviders(providersQ.data.catalog, providersQ.data.state) : []),
    [providersQ.data],
  )
  const alreadyDone = statusQ.data != null && !statusQ.data.needs_setup

  const [step, setStep] = useState<Step>('welcome')
  const [providerId, setProviderId] = useState<string | null>(null)
  const [apiKey, setApiKey] = useState('')
  const [rpm, setRpm] = useState('150')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState<ProviderWithState | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; detail?: string } | null>(null)

  const [models, setModels] = useState<CatalogModel[] | null>(null)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [modelsLoading, setModelsLoading] = useState(false)
  const [modelKey, setModelKey] = useState<string | null>(null)
  const [modelSaving, setModelSaving] = useState(false)
  const [modelSaveError, setModelSaveError] = useState<string | null>(null)
  const [modelSaved, setModelSaved] = useState<ModelSetEntry | null>(null)

  /* Default-select the first provider once the catalog loads. */
  useEffect(() => {
    if (providerId == null && providers.length > 0) {
      const preferred = providers.find((p) => p.id === 'meta') ?? providers[0]
      setProviderId(preferred.id)
      setRpm(String(preferred.rpm_limit ?? 150))
    }
  }, [providers, providerId])

  const selected = useMemo(
    () => providers.find((p) => p.id === providerId) ?? null,
    [providers, providerId],
  )

  const loadModels = async (): Promise<void> => {
    setModelsLoading(true)
    setModelsError(null)
    try {
      const catalog = await getCatalog()
      const filtered = catalog.models.filter((m) => isMetaFamilyModel(m.provider_id, m.model_id))
      setModels(filtered)
      const def = pickDefaultModel(filtered)
      if (def) setModelKey(`${def.provider_id}/${def.model_id}`)
    } catch (err) {
      setModelsError(err instanceof Error ? err.message : 'Loading models failed.')
    } finally {
      setModelsLoading(false)
    }
  }

  const goToModelStep = async (): Promise<void> => {
    setStep('model')
    if (models == null && !modelsLoading) await loadModels()
  }

  const saveProvider = async (): Promise<void> => {
    if (!selected) return
    setSaving(true)
    setSaveError(null)
    setTestResult(null)
    try {
      const key = apiKey.trim()
      const next = await updateProvider(selected.id, {
        enabled: true,
        ...(key !== '' ? { api_key: key } : {}),
        base_url_vars: { ...selected.base_url_vars },
        rpm_limit: rpm.trim() === '' ? null : Math.max(0, Math.round(Number(rpm))),
      })
      const merged: ProviderWithState = {
        ...selected,
        enabled: next.enabled,
        has_key: next.has_key,
        base_url_vars: next.base_url_vars,
        rpm_limit: next.rpm_limit,
      }
      setSaved(merged)
      setApiKey('')
      await providersQ.reload()
      await statusQ.reload()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Saving the provider failed.')
    } finally {
      setSaving(false)
    }
  }

  const runTest = async (): Promise<void> => {
    if (!selected) return
    setTesting(true)
    setTestResult(null)
    try {
      setTestResult(await testProvider(selected.id))
    } catch (err) {
      setTestResult({ ok: false, detail: err instanceof Error ? err.message : 'The test request failed.' })
    } finally {
      setTesting(false)
    }
  }

  const saveModel = async (): Promise<void> => {
    if (!modelKey) return
    const [entryProvider, ...rest] = modelKey.split('/')
    const entry: ModelSetEntry = { provider_id: entryProvider, model_id: rest.join('/') }
    setModelSaving(true)
    setModelSaveError(null)
    try {
      const defs = await listDefs('model_set')
      let defId = defs.items.find((d) => d.slug === 'default')?.id
      if (!defId) {
        const created = await createDef('model_set', {
          slug: 'default',
          name: 'Default model set',
          description: 'Default model (set by the setup wizard).',
          draft: { strategy: 'single', entries: [entry] },
        })
        defId = created.id
      } else {
        const editable = await getEditableDef('model_set', defId)
        const draft = { ...(editable.draft ?? { strategy: 'single' as const, entries: [] }), strategy: 'single' as const, entries: [entry] }
        await putDraft('model_set', defId, draft)
      }
      await publishDef('model_set', defId, 'setup wizard')
      setModelSaved(entry)
      await statusQ.reload()
    } catch (err) {
      setModelSaveError(err instanceof Error ? err.message : 'Saving the default model failed.')
    } finally {
      setModelSaving(false)
    }
  }

  const stepIndex = STEPS.findIndex((s) => s.id === step)
  const loading = providersQ.loading && providersQ.data === null
  const bootError = providersQ.error ?? statusQ.error

  return (
    <div className="mx-auto flex w-full max-w-2xl flex-col p-6">
      <PageHeader
        title="Set up Wonder Critique"
        description="Connect a provider, pick a Meta model, and you're ready to chat."
      />

      {/* Step indicator */}
      <ol className="mt-4 flex items-center gap-1.5" aria-label="Setup progress">
        {STEPS.map((s, i) => (
          <li key={s.id} className="flex min-w-0 flex-1 items-center gap-1.5">
            <span
              aria-current={s.id === step ? 'step' : undefined}
              className={cx(
                'grid h-6 w-6 shrink-0 place-items-center rounded-full text-xs font-medium tabular-nums',
                i < stepIndex && 'bg-accent-soft text-accent-ink',
                i === stepIndex && 'bg-accent text-white',
                i > stepIndex && 'bg-app text-ink-3',
              )}
            >
              {i < stepIndex ? '✓' : i + 1}
            </span>
            <span className={cx('truncate text-xs', i === stepIndex ? 'font-medium text-ink' : 'text-ink-3')}>
              {s.label}
            </span>
          </li>
        ))}
      </ol>

      <Loadable loading={loading} error={bootError} onRetry={() => { void providersQ.reload(); void statusQ.reload() }} className="mt-4">
        {alreadyDone && step === 'welcome' ? (
          <Card>
            <CardBody>
              <Callout tone="success" title="Already set up">
                A provider with an API key is configured. You can go straight to chat, or walk through
                the wizard again to change the provider or model.
              </Callout>
              <div className="mt-3 flex items-center gap-2">
                <Button variant="primary" onClick={() => navigate('/chat')}>Open chat</Button>
                <Button variant="secondary" onClick={() => setStep('provider')}>Reconfigure</Button>
              </div>
            </CardBody>
          </Card>
        ) : step === 'welcome' ? (
          <Card>
            <CardHeader title="Welcome" description="Three quick steps on first run" />
            <CardBody className="space-y-2 text-sm text-ink-2">
              <p><strong className="text-ink">1. Provider.</strong> Use OpenRouter or the Meta API — paste an API key and test the connection.</p>
              <p><strong className="text-ink">2. Model.</strong> Pick a Meta-family model. It becomes the <code>default</code> model set every agent runs on.</p>
              <p><strong className="text-ink">3. Done.</strong> Start chatting; change anything later under Settings → Providers or Studio → Model sets.</p>
              <div className="pt-2">
                <Button variant="primary" icon={<ArrowRight />} onClick={() => setStep('provider')}>
                  Get started
                </Button>
              </div>
            </CardBody>
          </Card>
        ) : step === 'provider' ? (
          <div className="space-y-3">
            <Card>
              <CardHeader title="Choose a provider" description="Only OpenRouter and Meta are available" />
              <CardBody className="space-y-2">
                {providers.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    onClick={() => { setProviderId(p.id); setSaved(null); setTestResult(null); setRpm(String(p.rpm_limit ?? 150)) }}
                    aria-pressed={p.id === providerId}
                    className={cx(
                      'flex w-full items-center gap-2 rounded-lg border px-3 py-2.5 text-left outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-accent/40',
                      p.id === providerId ? 'border-accent bg-accent-soft' : 'border-line bg-app/50 hover:bg-app',
                    )}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-base font-medium text-ink">{p.name}</span>
                      <span className="block truncate font-mono text-2xs text-ink-3">{p.id} · {p.model_count} Meta models</span>
                    </span>
                    {p.enabled && p.has_key && <Badge tone="success" dot>ready</Badge>}
                    <AdapterBadge adapter={p.adapter} />
                  </button>
                ))}
              </CardBody>
            </Card>

            {selected && (
              <Card>
                <CardHeader title="API key" description={selected.env ? `or set $${selected.env} on the server` : undefined} />
                <CardBody className="space-y-3">
                  <Field label="API key">
                    <div className="flex w-full items-center gap-2">
                      <Input
                        type="password"
                        autoComplete="new-password"
                        spellCheck={false}
                        icon={<KeyRound />}
                        placeholder={selected.has_key || saved?.has_key ? '•••••••• (unchanged)' : 'Paste API key…'}
                        value={apiKey}
                        onChange={(e) => setApiKey(e.target.value)}
                      />
                      {(selected.has_key || saved?.has_key) && <Badge tone="success">Configured</Badge>}
                    </div>
                  </Field>
                  <Field label="Requests/min" hint="Meta contributor mode limit is 150">
                    <Input type="number" min={1} step={1} className="max-w-28" value={rpm} onChange={(e) => setRpm(e.target.value)} />
                  </Field>
                  {saveError && <p className="text-xs text-danger">{saveError}</p>}
                  {testResult && (
                    <Callout tone={testResult.ok ? 'success' : 'danger'} title={testResult.ok ? 'Connection OK' : 'Connection failed'}>
                      {testResult.detail ?? (testResult.ok ? 'The provider answered a test request.' : undefined)}
                    </Callout>
                  )}
                  <div className="flex items-center gap-2">
                    <Button variant="primary" loading={saving} onClick={() => void saveProvider()}>
                      Save provider
                    </Button>
                    <Button
                      variant="secondary"
                      icon={<PlugZap />}
                      loading={testing}
                      disabled={!saved?.has_key && !selected.has_key}
                      title={saved?.has_key || selected.has_key ? undefined : 'Save an API key first'}
                      onClick={() => void runTest()}
                    >
                      Test connection
                    </Button>
                  </div>
                </CardBody>
              </Card>
            )}

            <WizardNav
              back={{ label: 'Back', onClick: () => setStep('welcome') }}
              next={{
                label: 'Continue to model',
                disabled: !(saved?.has_key ?? selected?.has_key ?? false),
                title: (saved?.has_key ?? selected?.has_key) ? undefined : 'Save an API key first',
                onClick: () => void goToModelStep(),
              }}
            />
          </div>
        ) : step === 'model' ? (
          <div className="space-y-3">
            <Card>
              <CardHeader
                title="Choose a model"
                description="Meta-family models from your enabled provider"
              />
              <CardBody className="space-y-2">
                {modelsLoading && <p className="text-sm text-ink-3">Loading models…</p>}
                {modelsError && (
                  <Callout tone="danger" title="Failed to load models">
                    <span className="mr-2">{modelsError}</span>
                    <Button size="sm" variant="secondary" onClick={() => void loadModels()}>Retry</Button>
                  </Callout>
                )}
                {models && models.length === 0 && !modelsError && (
                  <Callout tone="warning" title="No Meta models available">
                    Enable a provider with a key first, or refresh the provider catalog under Settings →
                    Providers.
                  </Callout>
                )}
                {models?.map((m) => {
                  const key = `${m.provider_id}/${m.model_id}`
                  const active = key === modelKey
                  return (
                    <button
                      key={key}
                      type="button"
                      onClick={() => { setModelKey(key); setModelSaved(null) }}
                      aria-pressed={active}
                      className={cx(
                        'flex w-full items-center gap-2 rounded-lg border px-3 py-2 text-left outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-accent/40',
                        active ? 'border-accent bg-accent-soft' : 'border-line bg-app/50 hover:bg-app',
                      )}
                    >
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-base font-medium text-ink">{m.display_name}</span>
                        <span className="block truncate font-mono text-2xs text-ink-3">{key}</span>
                      </span>
                      {m.capabilities.vision && <Badge tone="accent">vision</Badge>}
                      {m.capabilities.tools && <Badge tone="accent">tools</Badge>}
                    </button>
                  )
                })}
                {modelSaveError && <p className="text-xs text-danger">{modelSaveError}</p>}
                {modelSaved && (
                  <Callout tone="success" title="Default model saved">
                    {modelSaved.provider_id}/{modelSaved.model_id} is now the published <code>default</code> model set.
                  </Callout>
                )}
                <div className="flex items-center gap-2 pt-1">
                  <Button
                    variant="secondary"
                    icon={<RefreshCw />}
                    loading={modelsLoading}
                    onClick={() => void loadModels()}
                  >
                    Refresh models
                  </Button>
                  <Button variant="primary" loading={modelSaving} disabled={!modelKey} onClick={() => void saveModel()}>
                    Save as default model
                  </Button>
                </div>
              </CardBody>
            </Card>
            <WizardNav
              back={{ label: 'Back', onClick: () => setStep('provider') }}
              next={{
                label: modelSaved ? 'Finish' : 'Skip for now',
                disabled: false,
                onClick: () => setStep('done'),
              }}
            />
          </div>
        ) : (
          <Card>
            <CardBody className="space-y-3">
              <div className="flex items-center gap-2 text-ink">
                <CheckCircle2 size={18} className="text-success" />
                <h2 className="text-base font-semibold">Setup {modelSaved || (statusQ.data && !statusQ.data.needs_setup) ? 'complete' : 'almost complete'}</h2>
              </div>
              <StatusRow label="Provider" value={saved ? `${saved.id} (key saved)` : 'unchanged'} />
              {testResult && <StatusRow label="Connection test" value={testResult.ok ? 'passed' : `failed${testResult.detail ? ` — ${testResult.detail}` : ''}`} />}
              <StatusRow
                label="Default model"
                value={modelSaved ? `${modelSaved.provider_id}/${modelSaved.model_id}` : 'not changed (seeded default still applies)'}
              />
              {!modelSaved && (
                <Callout tone="warning" title="No model chosen">
                  Agents fall back to the seeded default model set. You can pick a model later under
                  Studio → Model sets.
                </Callout>
              )}
              <div className="flex items-center gap-2 pt-1">
                <Button variant="primary" onClick={() => navigate('/chat')}>Open chat</Button>
                <Button variant="secondary" onClick={() => navigate('/settings/providers')}>Provider settings</Button>
              </div>
            </CardBody>
          </Card>
        )}
      </Loadable>
    </div>
  )
}

function StatusRow({ label, value }: { label: string; value: string }) {
  return (
    <p className="text-sm text-ink-2">
      <span className="font-medium text-ink">{label}:</span> <span className="font-mono text-xs">{value}</span>
    </p>
  )
}

function WizardNav({
  back,
  next,
}: {
  back: { label: string; onClick: () => void }
  next: { label: string; disabled?: boolean; title?: string; onClick: () => void }
}) {
  return (
    <div className="flex items-center justify-between">
      <Button variant="secondary" icon={<ArrowLeft />} onClick={back.onClick}>
        {back.label}
      </Button>
      <Button variant="primary" icon={<ArrowRight />} disabled={next.disabled} title={next.title} onClick={next.onClick}>
        {next.label}
      </Button>
    </div>
  )
}
