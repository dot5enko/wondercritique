/**
 * Style guides (`/styleguides`): markdown design guides a vision model writes
 * from sample images. A chat picks one with its first message
 * (`StartRunBody.styleguide_id`); its agents follow it. Owner-scoped.
 */
import { apiBlob, apiDelete, apiGet, apiPatch, apiPost } from './client'

export type StyleguideStatus = 'generating' | 'ready' | 'failed'

export interface Styleguide {
  id: string
  name: string
  status: StyleguideStatus
  /** Why generation failed ("" otherwise). */
  error: string
  /** The user's hints for the generator. */
  notes: string
  markdown: string
  image_count: number
  created_at: string
  updated_at: string
}

/** Server limits (mirrors `styleguide.MaxImages` / `MaxImageBytes`). */
export const MAX_STYLEGUIDE_IMAGES = 4
export const MAX_STYLEGUIDE_IMAGE_BYTES = 8 << 20

/** GET /styleguides → the caller's guides, newest first. */
export async function listStyleguides(signal?: AbortSignal): Promise<Styleguide[]> {
  const res = await apiGet<{ items: Styleguide[] }>('/styleguides', { signal })
  return res.items ?? []
}

export function getStyleguide(id: string, signal?: AbortSignal): Promise<Styleguide> {
  return apiGet<Styleguide>(`/styleguides/${encodeURIComponent(id)}`, { signal })
}

/** POST /styleguides (multipart) → 201 in status `generating`; poll until it's done. */
export function createStyleguide(images: File[], name: string, notes: string): Promise<Styleguide> {
  const form = new FormData()
  for (const f of images) form.append('images', f, f.name)
  if (name.trim()) form.append('name', name.trim())
  if (notes.trim()) form.append('notes', notes.trim())
  return apiPost<Styleguide>('/styleguides', form)
}

/** PATCH /styleguides/{id}: rename and/or replace the markdown (a hand-written guide becomes ready). */
export function updateStyleguide(id: string, patch: { name?: string; markdown?: string }): Promise<Styleguide> {
  return apiPatch<Styleguide>(`/styleguides/${encodeURIComponent(id)}`, patch)
}

/** Generates the guide again from its sample images. */
export function regenerateStyleguide(id: string): Promise<Styleguide> {
  return apiPost<Styleguide>(`/styleguides/${encodeURIComponent(id)}/regenerate`)
}

/** Deletes the guide; chats that picked it keep following it. */
export function deleteStyleguide(id: string): Promise<void> {
  return apiDelete(`/styleguides/${encodeURIComponent(id)}`)
}

/** Object URL of sample image `n` (revoke it when done). */
export function styleguideImageURL(id: string, n: number, signal?: AbortSignal): Promise<string> {
  return apiBlob(`/styleguides/${encodeURIComponent(id)}/images/${n}`, { signal })
}
