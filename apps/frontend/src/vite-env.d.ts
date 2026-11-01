/// <reference types="vite/client" />

interface ImportMetaEnv {
  /**
   * Root of the backend API, including the `/api/v1` prefix.
   * Default (`''`) = same origin, so dev requests go through the Vite proxy
   * (`/api` → http://localhost:8080) and prod is served behind the same domain.
   */
  readonly VITE_API_BASE_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
