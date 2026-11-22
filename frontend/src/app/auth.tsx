import { type ReactNode, createContext, useCallback, useContext, useEffect, useState } from 'react'
import { api, setCsrf, setUnauthorizedHandler } from '../lib/api'
import type { Me } from '../lib/types'

interface AuthState {
  me: Me | null
  ready: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refresh: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null)
  const [ready, setReady] = useState(false)

  const accept = useCallback((m: Me | null) => {
    setCsrf(m?.csrf || '')
    setMe(m)
  }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => accept(null))
    api
      .get<Me>('auth/me')
      .then(accept)
      .catch(() => accept(null))
      .finally(() => setReady(true))
  }, [accept])

  const login = useCallback(
    async (username: string, password: string) => {
      accept(await api.post<Me>('auth/login', { username, password }))
    },
    [accept],
  )

  const refresh = useCallback(async () => {
    accept(await api.get<Me>('auth/me'))
  }, [accept])

  const logout = useCallback(async () => {
    try {
      await api.post('auth/logout')
    } finally {
      accept(null)
    }
  }, [accept])

  return <AuthContext.Provider value={{ me, ready, login, logout, refresh }}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('AuthProvider missing')
  return ctx
}

export function useMe(): Me {
  const { me } = useAuth()
  if (!me) throw new Error('not signed in')
  return me
}
