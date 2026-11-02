/**
 * Router root.
 *
 * Route table:
 *   /login                          public — email-code login, no shell
 *   /showcase                       dev-only design-system showcase (no shell)
 *   /          (auth shell)         → redirect to /chat
 *   /chat                           design chat: every chat = a pipeline run
 *   /runs/:id                       full run details (opened from the chat); /runs → /chat
 *   /agent-chat                     talk to a single published agent
 *   /studio/*, /settings            admin-only configuration
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
const RunDetailPage = lazy(() => import('./pages/runs/detail'))
const ChatPage = lazy(() => import('./pages/chat'))
const AgentChatPage = lazy(() => import('./pages/agent-chat'))
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
          { index: true, loader: () => redirect('/chat') },
          { path: 'chat', element: <ChatPage /> },
          { path: 'runs', loader: () => redirect('/chat') },
          { path: 'runs/:id', element: <RunDetailPage /> },
          { path: 'agent-chat', element: <AgentChatPage /> },
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
