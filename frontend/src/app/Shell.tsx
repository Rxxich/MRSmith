import { Link, NavLink, Outlet } from 'react-router-dom'
import { IconArrowsExchange, IconBox, IconLayoutDashboard, IconListDetails, IconLogout, IconSettings, IconStethoscope } from '@tabler/icons-react'
import { Notice } from '../ui/core'
import { useAuth } from './auth'

export function AnvilMark({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden>
      <path d="M3 10h20c0 3.6-2.7 6.1-6.3 6.6V19h3v3.5H9.3V19h3v-2.4C7.6 16 4.6 13.6 3 10Z" fill="var(--straw)" />
      <path d="M23 10h6c-.8 2-2.8 3.2-6.2 3.4.2-1.1.2-2.2.2-3.4Z" fill="var(--straw)" opacity=".65" />
      <rect x="7" y="24.5" width="15.3" height="2.5" rx="1" fill="var(--bronze)" />
    </svg>
  )
}

const NAV = [
  { to: '/', label: 'Обзор', icon: IconLayoutDashboard, end: true },
  { to: '/rulesets', label: 'Наборы правил', icon: IconListDetails },
  { to: '/convert', label: 'Конвертер', icon: IconArrowsExchange },
  { to: '/audit', label: 'Аудит', icon: IconStethoscope },
  { to: '/storage', label: 'Хранилище', icon: IconBox },
]

export function Shell() {
  const { me, logout } = useAuth()
  return (
    <div className="shell">
      <aside className="sidebar">
        <NavLink to="/" className="brand" aria-label="MRSmith — на главную">
          <AnvilMark />
          <span className="brand-name">
            MR<span>S</span>mith
          </span>
        </NavLink>
        <nav className="nav" aria-label="Разделы">
          {NAV.map(({ to, label, icon: Icon, end }) => (
            <NavLink key={to} to={to} end={end} title={label}>
              <Icon size={18} stroke={1.7} aria-hidden />
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          <nav className="nav" aria-label="Аккаунт">
            <NavLink to="/settings" title="Настройки">
              <IconSettings size={18} stroke={1.7} aria-hidden />
              <span>Настройки</span>
            </NavLink>
          </nav>
          <div className="who">
            <span className="truncate">
              {me?.user.username}
              <small className="who-version">v{me?.version}</small>
            </span>
            <button type="button" className="btn btn-ghost btn-sm btn-icon" onClick={() => void logout()} title="Выйти" aria-label="Выйти">
              <IconLogout size={16} />
            </button>
          </div>
        </div>
      </aside>
      <main className="main" id="main">
        {me?.user.mustChangePassword && (
          <div className="banner">
            <Notice tone="warn">
              Вы вошли с паролем, сгенерированным при установке. <Link to="/settings">Смените пароль</Link> — и, если хотите, логин.
            </Notice>
          </div>
        )}
        <Outlet />
      </main>
    </div>
  )
}
