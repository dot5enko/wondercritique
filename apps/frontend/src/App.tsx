/**
 * Router root.
 *
 * Route table:
 *   /login                          public — email-code login, no shell
 *   /showcase                       dev-only design-system showcase (no shell)
 *   /          (auth shell)         → redirect to /runs
 *   /runs, /runs/:id                placeholders
 *   /chat                           placeholder
 *   /studio/*, /settings            admin-only placeholders
 *
 * All pages are React.lazy code-split; `RequireAuth`/`RequireAdmin` guard the
 * shell and admin subtrees (src/features/auth.tsx).
 */
import { Suspense, lazy } from 'react'
import { Outlet, createBrowserRouter, redirect, RouterProvider } from 'react-router'
import { AuthProvider, BootScreen, RequireAdmin, RequireAuth } from './features/auth'
import Shell from './pages/shell'

const LoginPage = lazy(() => import('./pages/login'))
const ShowcasePage = lazy(() => import('./pages/showcase'))
const RunsPage = lazy(() => import('./pages/runs'))
const RunDetailPage = lazy(() => import('./pages/runs/detail'))
const ChatPage = lazy(() => import('./pages/chat'))
const SettingsPage = lazy(() => import('./pages/settings'))
const PipelinesPage = lazy(() => import('./pages/studio/pipelines'))
const AgentsPage = lazy(() => import('./pages/studio/agents'))
const ModelSetsPage = lazy(() => import('./pages/studio/model-sets'))
const SkillsPage = lazy(() => import('./pages/studio/skills'))
const RulesPage = lazy(() => import('./pages/studio/rules'))

const router = createBrowserRouter([
  {
    // Pathless root layout: auth state + the shared Suspense boundary.
    element: (
      <AuthProvider>
        <Suspense fallback={<BootScreen />}>
          <Outlet />
        </Suspense>
      </AuthProvider>
    ),
    children: [
      { path: '/login', element: <LoginPage /> },
      ...(import.meta.env.DEV ? [{ path: '/showcase', element: <ShowcasePage /> }] : []),
      {
        element: (
          <RequireAuth>
            <Shell />
          </RequireAuth>
        ),
        children: [
          { index: true, loader: () => redirect('/runs') },
          { path: 'runs', element: <RunsPage /> },
          { path: 'runs/:id', element: <RunDetailPage /> },
          { path: 'chat', element: <ChatPage /> },
          {
            path: 'studio/pipelines',
            element: (
              <RequireAdmin>
                <PipelinesPage />
              </RequireAdmin>
            ),
          },
          {
            path: 'studio/agents',
            element: (
              <RequireAdmin>
                <AgentsPage />
              </RequireAdmin>
            ),
          },
          {
            path: 'studio/model-sets',
            element: (
              <RequireAdmin>
                <ModelSetsPage />
              </RequireAdmin>
            ),
          },
          {
            path: 'studio/skills',
            element: (
              <RequireAdmin>
                <SkillsPage />
              </RequireAdmin>
            ),
          },
          {
            path: 'studio/rules',
            element: (
              <RequireAdmin>
                <RulesPage />
              </RequireAdmin>
            ),
          },
          {
            path: 'settings',
            element: (
              <RequireAdmin>
                <SettingsPage />
              </RequireAdmin>
            ),
          },
        ],
      },
    ],
  },
])

export default function App() {
  return <RouterProvider router={router} />
}
