/// <reference types="vite/client" />

interface ImportMetaEnv {
  /**
   * Origin of the backend API, without the `/api/v1` prefix (appended by
   * `api/client.ts`). Default (`''`) = same origin, so dev requests go through
   * the Vite proxy (`/api` → http://localhost:8080). Production sets it to the
   * backend domain at build time (Ansible `frontend_api_base_url`).
   */
  readonly VITE_API_BASE_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
