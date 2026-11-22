import { type FormEvent, useState } from 'react'
import { IconLogin } from '@tabler/icons-react'
import { useAuth } from '../app/auth'
import { AnvilMark } from '../app/Shell'
import { errorText } from '../lib/api'
import { useTitle } from '../lib/hooks'
import { Button, Field, Input, Notice } from '../ui/core'

export function Login() {
  useTitle('Вход')
  const { login } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await login(username.trim(), password)
    } catch (err) {
      setError(errorText(err))
      setPassword('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <form className="login-card" onSubmit={submit}>
        <div className="temper" aria-hidden />
        <div className="login-body">
          <div className="login-mark">
            <AnvilMark size={38} />
            <h1>
              MR<span>S</span>mith
            </h1>
          </div>
          <p className="login-lead">Наборы правил для mihomo: сборка, конвертация и хранение файлов.</p>
          {error && <Notice tone="error">{error}</Notice>}
          <Field label="Логин">
            {(id) => (
              <Input id={id} autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
            )}
          </Field>
          <Field label="Пароль">
            {(id) => (
              <Input
                id={id}
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            )}
          </Field>
          <Button type="submit" variant="primary" busy={busy} icon={<IconLogin size={17} />}>
            Войти
          </Button>
        </div>
      </form>
    </div>
  )
}
