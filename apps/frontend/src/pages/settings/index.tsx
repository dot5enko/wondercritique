/**
 * Admin settings — LLM providers (plan §3.1–§3.3, §4.7).
 *
 * Two-pane layout: the models.dev provider catalog on the left (search,
 * enabled-first ordering, catalog sync state + refresh) and a configuration
 * pane for the selected provider on the right (enable switch, write-only API
 * key, base-URL template variables, requests/min limit, connection test).
 *
 * The Go backend for these routes does not exist yet — loading, error and
 * empty states render gracefully against 404/502.
 */
import { useEffect, useMemo, useState } from 'react'
import { Boxes, PlugZap, RefreshCw, Search } from 'lucide-react'
import {
  getProviders,
  mergeProviders,
  refreshProviderCatalog,
  type ProviderWithState,
} from '../../api/settings'
import { Loadable, PageHeader, absoluteTime, relativeTime } from '../../components'
import { useAsync } from '../../hooks'
import { Badge, Button, Callout, EmptyState, Input, cx } from '../../ui'
import ProviderDetail from './ProviderDetail'
import { AdapterBadge } from './parts'

export default function SettingsPage() {
  const { data, loading, error, reload, setData } = useAsync(getProviders, [])

  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [refreshError, setRefreshError] = useState<string | null>(null)

  const providers = useMemo(() => (data ? mergeProviders(data.catalog, data.state) : []), [data])
  const enabledCount = useMemo(() => providers.reduce((n, p) => n + (p.enabled ? 1 : 0), 0), [providers])

  /* Enabled providers pinned on top; catalog order preserved within each group.
     No "show deprecated" toggle: ProviderCatalogEntry carries no deprecated flag
     (models.dev marks models, not providers, deprecated). */
  const sorted = useMemo(() => [...providers].sort((a, b) => Number(b.enabled) - Number(a.enabled)), [providers])

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () => (q ? sorted.filter((p) => p.name.toLowerCase().includes(q) || p.id.toLowerCase().includes(q)) : sorted),
    [sorted, q],
  )

  const selected = useMemo(
    () => (selectedId ? providers.find((p) => p.id === selectedId) : undefined),
    [providers, selectedId],
  )

  /* Clear a selection that vanished from the catalog (e.g. after a refresh). */
  useEffect(() => {
    if (selectedId && !selected) setSelectedId(null)
  }, [selectedId, selected])

  const refreshCatalog = async (): Promise<void> => {
    setRefreshing(true)
    setRefreshError(null)
    try {
      setData(await refreshProviderCatalog())
    } catch (err) {
      setRefreshError(err instanceof Error ? err.message : 'Catalog refresh failed.')
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div className="flex h-full flex-col p-6">
      <PageHeader
        title="LLM providers"
        description="Enable providers, store API keys and set rate limits — sourced from the models.dev catalog."
      />
      <Callout tone="info" className="mt-3 shrink-0">
        The provider catalog syncs from models.dev. API keys are stored AES-GCM-encrypted and never returned —
        set a key per provider here, or provide the provider's environment variable on the server as a fallback.
      </Callout>

      <Loadable loading={loading && data === null} error={error} onRetry={reload} className="mt-4 min-h-0 flex-1">
        <div className="grid h-full min-h-0 grid-cols-[340px_minmax(0,1fr)] gap-4">
          {/* Left — provider catalog */}
          <aside className="flex min-h-0 flex-col overflow-hidden rounded-lg border border-line bg-surface">
            <div className="shrink-0 border-b border-line px-3 py-2">
              <Input
                size="sm"
                icon={<Search size={13} />}
                placeholder="Search providers…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>

            {/* Catalog header row: last sync state (when exposed) + refresh */}
            <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-2xs text-ink-3">
              <span
                className="min-w-0 flex-1 truncate"
                title={data?.synced_at ? absoluteTime(data.synced_at) : undefined}
              >
                models.dev catalog
                {data?.synced_at ? ` · synced ${relativeTime(data.synced_at)}` : ''}
              </span>
              <Button
                size="sm"
                variant="ghost"
                icon={<RefreshCw />}
                loading={refreshing}
                onClick={() => void refreshCatalog()}
              >
                Refresh catalog
              </Button>
            </div>
            {refreshError && (
              <p className="shrink-0 border-b border-line px-3 py-1.5 text-xs text-danger">{refreshError}</p>
            )}

            <div className="min-h-0 flex-1 overflow-y-auto">
              {visible.length === 0 ? (
                <EmptyState
                  icon={q ? Search : Boxes}
                  title={q ? `No providers match “${query.trim()}”` : 'No providers in the catalog'}
                  description={q ? undefined : 'Refresh the catalog or check back later.'}
                />
              ) : (
                <div className="divide-y divide-line">
                  {visible.map((p) => (
                    <ProviderRow
                      key={p.id}
                      provider={p}
                      selected={p.id === selectedId}
                      onSelect={() => setSelectedId(p.id)}
                    />
                  ))}
                </div>
              )}
            </div>

            <div className="shrink-0 border-t border-line px-3 py-1.5 text-2xs text-ink-3">
              {q ? `${visible.length} of ${providers.length} shown` : `${providers.length} providers · ${enabledCount} enabled`}
            </div>
          </aside>

          {/* Right — selected provider, or a muted hint panel */}
          {selected ? (
            <ProviderDetail key={selected.id} provider={selected} onSaved={reload} />
          ) : (
            <section className="grid place-items-center rounded-lg border border-line bg-app/40">
              <EmptyState
                icon={PlugZap}
                title="Select a provider"
                description="Pick a provider on the left to enable it and configure its API key, base URL and rate limit."
              />
            </section>
          )}
        </div>
      </Loadable>
    </div>
  )
}

/** One catalog row: name, enabled badge, adapter badge, model count. */
function ProviderRow({
  provider,
  selected,
  onSelect,
}: {
  provider: ProviderWithState
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected || undefined}
      className={cx(
        'flex w-full cursor-pointer items-center gap-2 px-3 py-2 text-left outline-none transition-colors duration-100 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent/40',
        selected ? 'bg-accent-soft' : 'hover:bg-app',
      )}
    >
      <span className={cx('min-w-0 flex-1 truncate text-base', selected ? 'font-medium text-accent-ink' : 'text-ink')}>
        {provider.name}
      </span>
      {provider.enabled && (
        <Badge tone="success" dot>
          enabled
        </Badge>
      )}
      <AdapterBadge adapter={provider.adapter} />
      <span
        className="w-10 shrink-0 text-right text-2xs tabular-nums text-ink-3"
        title={`${provider.model_count} models`}
      >
        {provider.model_count}
      </span>
    </button>
  )
}
