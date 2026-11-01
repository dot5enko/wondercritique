/**
 * Auth API — email-code login against `/auth/*` (docs/backend.md "Auth").
 *
 * The built-in provider is `email_code`; endpoints are provider-parameterized
 * so a future provider only needs the path prefix changed.
 */
import { apiGet, apiPost, clearSession, getSession, setSession } from './client'
import type { TokenPair, User } from './types'

export type { TokenPair, User } from './types'

const PROVIDER = 'email_code'

/** POST /auth/{provider}/start → the code was sent, valid for `expires_in` seconds. */
export interface EmailCodeStart {
  sent: boolean
  expires_in: number
}

/**
 * Starts the email-code flow. A 429 (resend cooldown / per-IP rate limit)
 * throws {@link ApiError} with `retryAfter` seconds parsed from `Retry-After`.
 */
export async function startEmailCode(email: string, signal?: AbortSignal): Promise<EmailCodeStart> {
  return apiPost<EmailCodeStart>(`/auth/${PROVIDER}/start`, { email }, { noAuth: true, signal })
}

/**
 * Verifies the 6-digit code and stores the returned session (tokens + user)
 * in localStorage. Bad/expired codes throw a 401 {@link ApiError} with the
 * backend's constant message `invalid or expired code`.
 */
export async function verifyEmailCode(email: string, code: string, signal?: AbortSignal): Promise<User> {
  const pair = await apiPost<TokenPair>(
    `/auth/${PROVIDER}/verify`,
    { email, code },
    { noAuth: true, signal },
  )
  setSession({ access_token: pair.access_token, refresh_token: pair.refresh_token, user: pair.user })
  return pair.user
}

/** Fetches the current user with the stored access token (401 → anonymous). */
export function me(signal?: AbortSignal): Promise<User> {
  return apiGet<User>('/auth/me', { signal })
}

/**
 * Revokes the stored refresh token server-side and clears the local session.
 * Never throws: an already-invalid token is fine to log out from.
 */
export async function logout(): Promise<void> {
  const refresh_token = getSession()?.refresh_token
  try {
    if (refresh_token) await apiPost<void>('/auth/logout', { refresh_token }, { noAuth: true })
  } finally {
    clearSession()
  }
}
