import {
  NavLink,
  Navigate,
  Route,
  Routes,
  useLocation,
} from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from './auth/context'
import LoginPage from './pages/LoginPage'
import ChampionsPage from './pages/ChampionsPage'
import TasksPage from './pages/TasksPage'
import ChallengerReviewPage from './pages/ChallengerReviewPage'
import InstancesPage from './pages/InstancesPage'
import InstanceLivePage from './pages/InstanceLivePage'

// "Analysis" is a deep link out to optuna-dashboard, per intent (a): the
// native UI never rebuilds the analysis scene.
const OPTUNA_URL = 'http://192.168.67.129:8088/'

function NavItem({ to, label }: { to: string; label: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `px-3 py-2 rounded-md text-sm font-medium ${
          isActive
            ? 'bg-slate-900 text-white'
            : 'text-slate-600 hover:bg-slate-100'
        }`
      }
    >
      {label}
    </NavLink>
  )
}

function Placeholder({ title }: { title: string }) {
  return (
    <div className="rounded-lg border border-slate-200 p-8 text-slate-500">
      <h2 className="text-lg font-semibold text-slate-800">{title}</h2>
      <p className="mt-1 text-sm">Placeholder — wired in a later F1 step.</p>
    </div>
  )
}

// Redirect to /login when there's no standing session, remembering where
// the user was headed.
function RequireAuth({ children }: { children: ReactNode }) {
  const { auth } = useAuth()
  const location = useLocation()
  if (!auth)
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  return <>{children}</>
}

function Shell({ children }: { children: ReactNode }) {
  const { auth, logout } = useAuth()
  return (
    <div className="min-h-full bg-slate-50 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex max-w-6xl items-center gap-2 px-6 py-3">
          <span className="mr-4 font-semibold tracking-tight">QuantLab</span>
          <NavItem to="/champions" label="Champions" />
          <NavItem to="/tasks" label="Tasks" />
          <NavItem to="/instances" label="Live" />
          <a
            href={OPTUNA_URL}
            target="_blank"
            rel="noreferrer"
            className="px-3 py-2 rounded-md text-sm font-medium text-slate-600 hover:bg-slate-100"
          >
            Analysis ↗
          </a>
          <div className="ml-auto flex items-center gap-3 text-sm text-slate-500">
            <span className="rounded bg-slate-100 px-2 py-0.5 text-xs">
              {auth?.role}
            </span>
            <button
              type="button"
              onClick={logout}
              className="text-slate-600 hover:text-slate-900"
            >
              Sign out
            </button>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-6 py-8">{children}</main>
    </div>
  )
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/*"
        element={
          <RequireAuth>
            <Shell>
              <Routes>
                <Route
                  path="/"
                  element={<Navigate to="/champions" replace />}
                />
                <Route path="/champions" element={<ChampionsPage />} />
                <Route path="/tasks" element={<TasksPage />} />
                <Route path="/tasks/:taskId" element={<ChallengerReviewPage />} />
                <Route path="/instances" element={<InstancesPage />} />
                <Route
                  path="/instances/:instanceId"
                  element={<InstanceLivePage />}
                />
                <Route path="*" element={<Placeholder title="Not found" />} />
              </Routes>
            </Shell>
          </RequireAuth>
        }
      />
    </Routes>
  )
}
