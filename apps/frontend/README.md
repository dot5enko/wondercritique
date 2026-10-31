# Frontend

Vite + TypeScript + React (bare scaffold — no router, state library, or linter yet).

## Commands

```sh
npm install
npm run dev      # dev server with HMR (http://localhost:5173)
npm run build    # type-check (tsc -b) + production build to dist/
npm run preview  # serve the production build
```

Stack: Vite 8, React 19, TypeScript 7. Layout follows the standard `create-vite` react-ts
template: `src/App.tsx` (root component), `src/main.tsx` (entry), `vite.config.ts`,
`tsconfig.app.json` (app code) / `tsconfig.node.json` (build config, project references).
