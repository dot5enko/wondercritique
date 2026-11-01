/**
 * Types shared across API modules (kept dependency-free so every module and
 * feature can import from here without cycles).
 */

/** Backend user. The backend may add fields; only these are relied upon. */
export interface User {
  id: string
  email: string
  name: string
  role: string
}

/** Token pair returned by `/auth/verify` and `/auth/refresh` (docs/backend.md "Auth"). */
export interface TokenPair {
  access_token: string
  refresh_token: string
  token_type: string
  expires_in: number
  user: User
}

/** Token usage as reported by agentkit (`llm.Usage`). */
export interface TokenUsage {
  input_tokens: number
  cached_input_tokens: number
  output_tokens: number
  reasoning_tokens: number
}
