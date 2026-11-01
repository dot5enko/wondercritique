/**
 * Admin provider settings — models.dev catalog + enabled-provider state
 * (plan §3.1–§3.3, §4.7). Mounted at `/api/v1/settings/providers`, admin only.
 *
 * API keys are write-only: the backend never returns them (`has_key` only).
 */
import { apiGet, apiPost, apiPut } from './client'

/** Adapter family from models.dev `npm`/`shape` metadata (plan §3.2). */
export type ProviderAdapter = 'responses' | 'chat' | 'anthropic' | 'none'

/**
 * One provider as parsed from the models.dev snapshot.
 * ASSUMPTION: `adapter` is resolved server-side from npm/shape per §3.2;
 * `model_count` is the number of chat models kept after filtering.
 */
export interface ProviderCatalogEntry {
  id: string
  name: string
  /** npm package the adapter maps from, e.g. `@ai-sdk/openai`; absent for some gateways. */
  npm?: string
  /** Wire protocol used by the agentkit adapter; `none` = listed but cannot be enabled. */
  adapter: ProviderAdapter
  /** Env var name from models.dev, e.g. `META_MODEL_API_KEY` (key fallback). */
  env?: string
  /** API base URL template with `${VAR}` placeholders — the vars become settings fields. */
  api?: string
  /** Provider docs URL from models.dev (`doc`) — rendered as a link-out hint. */
  doc?: string
  model_count: number
}

/** Enabled/state row for one provider (`llm_providers`, plan §3.3). */
export interface ProviderState {
  provider_id: string
  enabled: boolean
  /** True when a key is stored or an env fallback exists — the key itself is never returned. */
  has_key: boolean
  base_url_vars: Record<string, string>
  /** Requests-per-minute limit; null = unlimited. */
  rpm_limit: number | null
}

export interface ProvidersResponse {
  /** ISO timestamp of the last models.dev catalog sync, when the backend exposes it. */
  synced_at?: string
  catalog: ProviderCatalogEntry[]
  state: ProviderState[]
}

export function getProviders(signal?: AbortSignal): Promise<ProvidersResponse> {
  return apiGet<ProvidersResponse>('/settings/providers', { signal })
}

/** Catalog entry joined with its state row, ordered as the catalog lists them. */
export interface ProviderWithState extends ProviderCatalogEntry, Omit<ProviderState, 'provider_id'> {}

/** Joins the models.dev catalog with the enabled/state rows into one list. */
export function mergeProviders(catalog: ProviderCatalogEntry[], state: ProviderState[]): ProviderWithState[] {
  const byId = new Map(state.map((s) => [s.provider_id, s]))
  return catalog.map((entry) => {
    const s = byId.get(entry.id)
    return {
      enabled: false,
      has_key: false,
      base_url_vars: {},
      rpm_limit: null,
      ...entry,
      ...(s ? { enabled: s.enabled, has_key: s.has_key, base_url_vars: s.base_url_vars, rpm_limit: s.rpm_limit } : {}),
    }
  })
}

/** Fetches and merges catalog + state into one list for the settings UI. */
export async function listProviders(signal?: AbortSignal): Promise<ProviderWithState[]> {
  const { catalog, state } = await getProviders(signal)
  return mergeProviders(catalog, state)
}

/** Body of `updateProvider` — omit `api_key` to keep the stored key. */
export interface ProviderUpdate {
  enabled: boolean
  api_key?: string
  base_url_vars: Record<string, string>
  rpm_limit?: number | null
}

/**
 * PUT /settings/providers/{provider_id} — enables/disables a provider, sets
 * the API key (write-only), base-URL template vars and the rpm limit.
 * Omit `api_key` to keep the stored one. ASSUMPTION: answers with the updated
 * state row.
 */
export function updateProvider(
  providerId: string,
  body: ProviderUpdate,
  signal?: AbortSignal,
): Promise<ProviderState> {
  return apiPut<ProviderState>(`/settings/providers/${providerId}`, body, { signal })
}

/** POST /settings/providers/{provider_id}/test — tiny request against the provider. */
export function testProvider(providerId: string, signal?: AbortSignal): Promise<{ ok: boolean; detail?: string }> {
  return apiPost<{ ok: boolean; detail?: string }>(`/settings/providers/${providerId}/test`, undefined, { signal })
}

/**
 * POST /settings/providers/catalog/refresh — re-fetches models.dev now.
 * ASSUMPTION: answers with the refreshed catalog + state (same shape as GET).
 */
export function refreshProviderCatalog(signal?: AbortSignal): Promise<ProvidersResponse> {
  return apiPost<ProvidersResponse>('/settings/providers/catalog/refresh', undefined, { signal })
}
