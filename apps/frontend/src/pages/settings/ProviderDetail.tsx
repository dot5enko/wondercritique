/**
 * Right pane of the LLM provider settings page (plan §3.2–§3.3): enable
 * switch, write-only API key, base-URL template variables and the rpm limit,
 * plus "Test connection".
 *
 * Mount with `key={provider.id}`: switching providers re-seeds the draft,
 * while useDirtyDraft only adopts reloaded server state when the draft has no
 * unsaved edits.
 */
import { useState } from 'react'
import { ExternalLink, KeyRound, PlugZap } from 'lucide-react'
import {
  testProvider,
  updateProvider,
  type ProviderUpdate,
  type ProviderWithState,
} from '../../api/settings'
import { useDirtyDraft } from '../../hooks'
import { Badge, Button, Callout, Field, Input, Switch } from '../../ui'
import { AdapterBadge, CopyButton, apiVarNames } from './parts'

/** Meta contributor mode default (plan §3.3). */
const DEFAULT_RPM = 150

interface ProviderDraft {
  enabled: boolean
  /** Empty = keep the stored key — keys are write-only and never returned. */
  apiKey: string
  baseUrlVars: Record<string, string>
  /** The number input edits text; empty on save → null (unlimited). */
  rpm: string
}

function draftFrom(provider: ProviderWithState): ProviderDraft {
  return {
    enabled: provider.enabled,
    apiKey: '',
    baseUrlVars: { ...provider.base_url_vars },
    rpm: String(provider.rpm_limit ?? DEFAULT_RPM),
  }
}

export default function ProviderDetail({
  provider,
  onSaved,
}: {
  provider: ProviderWithState
  onSaved: () => void
}) {
  const { draft, setDraft, dirty, reset } = useDirtyDraft<ProviderDraft>(draftFrom(provider))
  const vars = apiVarNames(provider.api)
  const canEnable = provider.adapter !== 'none'

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; detail?: string } | null>(null)

  /* The server tests its stored credentials, so this gates on the saved
     enabled state; a just-typed (not yet saved) key still counts. */
  const canTest = provider.enabled && (provider.has_key || draft.apiKey.trim() !== '')

  const save = async (): Promise<void> => {
    setSaving(true)
    setSaveError(null)
    try {
      const rpm = Number(draft.rpm)
      const body: ProviderUpdate = {
        enabled: draft.enabled,
        /* Send the full record — the keys are stored/returned server-side. */
        base_url_vars: {
          ...draft.baseUrlVars,
          ...Object.fromEntries(vars.map((name) => [name, draft.baseUrlVars[name] ?? ''])),
        },
        rpm_limit:
          draft.rpm.trim() === '' || !Number.isFinite(rpm) ? null : Math.max(0, Math.round(rpm)),
      }
      const key = draft.apiKey.trim()
      if (key !== '') body.api_key = key

      const next = await updateProvider(provider.id, body)
      /* Adopt the saved state immediately and clear the key field. */
      reset({
        enabled: next.enabled,
        apiKey: '',
        baseUrlVars: { ...next.base_url_vars },
        rpm: String(next.rpm_limit ?? DEFAULT_RPM),
      })
      onSaved()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Saving the provider failed.')
    } finally {
      setSaving(false)
    }
  }

  const runTest = async (): Promise<void> => {
    setTesting(true)
    setTestResult(null)
    try {
      setTestResult(await testProvider(provider.id))
    } catch (err) {
      setTestResult({ ok: false, detail: err instanceof Error ? err.message : 'The test request failed.' })
    } finally {
      setTesting(false)
    }
  }

  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-line bg-surface">
      <header className="shrink-0 border-b border-line px-4 py-3">
        <div className="flex items-center gap-2">
          <h2 className="min-w-0 flex-1 truncate text-base font-semibold text-ink">{provider.name}</h2>
          <AdapterBadge adapter={provider.adapter} />
        </div>
        <div className="mt-1 flex items-center gap-1 text-2xs text-ink-3">
          <code className="min-w-0 truncate font-mono" title={provider.id}>
            {provider.id}
          </code>
          <CopyButton value={provider.id} label="Copy provider ID" />
          {provider.doc && (
            <a
              href={provider.doc}
              target="_blank"
              rel="noreferrer"
              className="ml-auto inline-flex shrink-0 items-center gap-1 transition-colors duration-100 hover:text-ink"
            >
              <ExternalLink size={11} />
              models.dev
            </a>
          )}
        </div>
      </header>

      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        {/* Enable — providers without an adapter cannot be enabled (plan §3.2). */}
        <div className="flex items-center justify-between gap-3 rounded-lg border border-line bg-app/50 px-3 py-2.5">
          <div className="min-w-0">
            <p className="text-base font-medium text-ink">Enabled</p>
            <p className="text-xs text-ink-3">
              {canEnable ? 'Usable by agents and model sets.' : 'No adapter yet — listed but cannot be enabled.'}
            </p>
          </div>
          <Switch
            checked={draft.enabled}
            onChange={(enabled) => setDraft((s) => ({ ...s, enabled }))}
            disabled={!canEnable}
          />
        </div>

        {/* API key (write-only) with env-var fallback hint from the catalog. */}
        <Field label="API key" hint={provider.env ? `or set $${provider.env} on the server` : undefined}>
          <div className="flex w-full items-center gap-2">
            <Input
              type="password"
              autoComplete="new-password"
              spellCheck={false}
              icon={<KeyRound />}
              placeholder={provider.has_key ? '•••••••• (unchanged)' : 'Paste API key…'}
              value={draft.apiKey}
              onChange={(e) => setDraft((s) => ({ ...s, apiKey: e.target.value }))}
            />
            {provider.has_key && <Badge tone="success">Configured</Badge>}
          </div>
        </Field>

        {/* Base URL template vars — one field per `${VAR}` placeholder. */}
        {provider.api ? (
          <div className="space-y-3">
            <p className="text-xs font-medium text-ink-2">Base URL variables</p>
            {vars.length === 0 ? (
              <p className="text-xs text-ink-3">Fixed base URL from the catalog — no variables to set.</p>
            ) : (
              vars.map((name) => (
                <Field key={name} label={name} hint={provider.api}>
                  <Input
                    spellCheck={false}
                    value={draft.baseUrlVars[name] ?? ''}
                    onChange={(e) =>
                      setDraft((s) => ({ ...s, baseUrlVars: { ...s.baseUrlVars, [name]: e.target.value } }))
                    }
                  />
                </Field>
              ))
            )}
          </div>
        ) : (
          <p className="text-xs text-ink-3">No base URL template — this provider uses its SDK default endpoint.</p>
        )}

        {/* Rate limit */}
        <Field label="Requests/min" hint="Meta contributor mode limit is 150">
          <Input
            type="number"
            min={1}
            step={1}
            className="max-w-28"
            value={draft.rpm}
            onChange={(e) => setDraft((s) => ({ ...s, rpm: e.target.value }))}
          />
        </Field>
      </div>

      <footer className="shrink-0 space-y-2.5 border-t border-line px-4 py-3">
        {testResult &&
          (testResult.ok ? (
            <Callout tone="success" title="Connection OK">
              {testResult.detail ?? 'The provider answered a test request.'}
            </Callout>
          ) : (
            <Callout tone="danger" title="Connection failed">
              {testResult.detail ?? 'The provider did not answer the test request.'}
            </Callout>
          ))}
        {saveError && <p className="text-xs text-danger">{saveError}</p>}
        <div className="flex items-center gap-2">
          <Button variant="primary" loading={saving} disabled={!dirty} onClick={() => void save()}>
            Save changes
          </Button>
          <Button
            variant="secondary"
            icon={<PlugZap />}
            loading={testing}
            disabled={!canTest || saving}
            title={canTest ? undefined : 'Enable the provider and set an API key first'}
            onClick={() => void runTest()}
          >
            Test connection
          </Button>
        </div>
      </footer>
    </section>
  )
}
