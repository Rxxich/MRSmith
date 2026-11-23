import { type FormEvent, useState } from 'react'
import { IconDeviceDesktop, IconKey, IconLogout, IconSend } from '@tabler/icons-react'
import { useAuth, useMe } from '../app/auth'
import { api, errorText } from '../lib/api'
import { count, dateTime, describeBrowser, fmtBytes, publicBase, relTime } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { Integrations, ProxyInfo, SessionInfo } from '../lib/types'
import { Button, Field, Input, Loading, Notice } from '../ui/core'
import { useOverlay } from '../ui/overlay'
import { BackupsPanel } from './Backups'
import { Journal } from './Journal'

export function Settings() {
  useTitle('Настройки')
  const me = useMe()
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Настройки</h1>
          <p>Вход в панель, бэкапы, связь с GitHub и Telegram, активные сессии и кто скачивал файлы.</p>
        </div>
      </div>
      <div className="grid-2">
        <div className="stack">
          <PasswordForm />
          <UsernameForm />
        </div>
        <div className="stack">
          <section className="panel">
            <div className="panel-head">
              <h2>Сервер</h2>
            </div>
            <div className="panel-body">
              <dl className="facts">
                <dt>Версия</dt>
                <dd className="mono">{me.version}</dd>
                <dt>Адрес ссылок</dt>
                <dd className="mono truncate">{publicBase(me)}</dd>
                <dt>Путь панели</dt>
                <dd className="mono">{me.panelPath}</dd>
                <dt>Лимит загрузки файла</dt>
                <dd>{fmtBytes(me.limits.maxUpload)}</dd>
                <dt>Лимит скачивания источника</dt>
                <dd>{fmtBytes(me.limits.maxFetch)}</dd>
              </dl>
              <p className="small muted mt-s">Эти параметры задаются переменными окружения MRSMITH_* в docker-compose.</p>
            </div>
          </section>
          <IntegrationsPanel />
        </div>
      </div>
      <BackupsPanel />
      <Sessions />
      <Journal />
    </div>
  )
}

function proxyText(p: ProxyInfo) {
  return `${p.url}${p.auth ? ' с логином' : ''}, ${p.remoteDns ? 'DNS на стороне прокси' : 'DNS на этом сервере'}`
}

function IntegrationsPanel() {
  const { toast } = useOverlay()
  const info = useAsync(() => api.get<Integrations>('integrations'), [])
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const test = async () => {
    setError('')
    setSending(true)
    try {
      await api.post('integrations/telegram/test')
      toast('Сообщение отправлено, проверьте Telegram')
    } catch (e) {
      setError(errorText(e))
    } finally {
      setSending(false)
    }
  }
  const d = info.data
  return (
    <section className="panel">
      <div className="panel-head">
        <div>
          <h2>GitHub и Telegram</h2>
          <p>Прокси для загрузки с GitHub и уведомления об изменениях</p>
        </div>
      </div>
      {!d ? (
        <Loading />
      ) : (
        <>
          <div className="panel-body stack">
            {error && <Notice tone="error">{error}</Notice>}
            <dl className="facts">
              <dt>Загрузка с GitHub</dt>
              <dd className={d.githubProxy ? 'mono-soft' : 'muted'}>{d.githubProxy ? `через ${proxyText(d.githubProxy)}` : 'напрямую'}</dd>
              <dt>Telegram</dt>
              <dd className={d.telegram.enabled ? undefined : 'muted'}>
                {d.telegram.enabled ? `включён, ${count(d.telegram.chats, ['получатель', 'получателя', 'получателей'])}` : 'выключен'}
              </dd>
              {d.telegram.enabled && (
                <>
                  <dt>Связь с Telegram</dt>
                  <dd className={d.telegram.proxy ? 'mono-soft' : 'muted'}>{d.telegram.proxy ? `через ${proxyText(d.telegram.proxy)}` : 'напрямую'}</dd>
                </>
              )}
            </dl>
            <p className="small muted">
              Задаются в .env: MRSMITH_GITHUB_PROXY, TELEGRAM_TOKEN, ADMIN_ID и TELEGRAM_PROXY. Прокси указывается как socks5:// или socks5h://. После правки перезапустите контейнер.
            </p>
          </div>
          {d.telegram.enabled && (
            <div className="panel-foot">
              <Button variant="secondary" busy={sending} onClick={test} icon={<IconSend size={16} />}>
                Отправить проверочное сообщение
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  )
}

function UsernameForm() {
  const me = useMe()
  const { refresh } = useAuth()
  const { toast } = useOverlay()
  const [username, setUsername] = useState(me.user.username)
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await api.post('auth/username', { username: username.trim(), password })
      await refresh()
      toast('Логин изменён')
      setPassword('')
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="panel" onSubmit={submit}>
      <div className="panel-head">
        <div>
          <h2>Логин</h2>
          <p>При установке он генерируется случайно — можно заменить на свой</p>
        </div>
      </div>
      <div className="panel-body stack">
        {error && <Notice tone="error">{error}</Notice>}
        <Field label="Новый логин" hint="От 3 до 64 символов: латиница, цифры, точка, дефис, подчёркивание">
          {(id) => <Input id={id} autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required pattern="[a-zA-Z0-9._\-]{3,64}" />}
        </Field>
        <Field label="Текущий пароль">
          {(id) => <Input id={id} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />}
        </Field>
      </div>
      <div className="panel-foot">
        <Button type="submit" variant="secondary" busy={busy} disabled={username.trim() === me.user.username}>
          Сменить логин
        </Button>
      </div>
    </form>
  )
}

function PasswordForm() {
  const me = useMe()
  const { refresh } = useAuth()
  const { toast } = useOverlay()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    if (next !== repeat) {
      setError('Новый пароль и повтор не совпадают')
      return
    }
    setBusy(true)
    try {
      await api.post('auth/password', { current, next })
      await refresh()
      toast('Пароль изменён, остальные сессии завершены')
      setCurrent('')
      setNext('')
      setRepeat('')
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="panel" onSubmit={submit}>
      <div className="panel-head">
        <div>
          <h2>Пароль</h2>
          <p>Последняя смена {relTime(me.user.passwordChangedAt)}</p>
        </div>
      </div>
      <div className="panel-body stack">
        {error && <Notice tone="error">{error}</Notice>}
        <input type="text" autoComplete="username" value={me.user.username} readOnly hidden />
        <Field label="Текущий пароль">
          {(id) => <Input id={id} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />}
        </Field>
        <Field label="Новый пароль" hint="Не короче 10 символов. Удобнее всего — фраза из нескольких слов.">
          {(id) => <Input id={id} type="password" autoComplete="new-password" minLength={10} value={next} onChange={(e) => setNext(e.target.value)} required />}
        </Field>
        <Field label="Повторите новый пароль">
          {(id) => <Input id={id} type="password" autoComplete="new-password" minLength={10} value={repeat} onChange={(e) => setRepeat(e.target.value)} required />}
        </Field>
      </div>
      <div className="panel-foot">
        <Button type="submit" variant="primary" busy={busy} icon={<IconKey size={17} />}>
          Сменить пароль
        </Button>
      </div>
    </form>
  )
}

function Sessions() {
  const { toast } = useOverlay()
  const list = useAsync(() => api.get<SessionInfo[]>('auth/sessions'), [])
  const revoke = async (s: SessionInfo) => {
    try {
      await api.del(`auth/sessions/${s.id}`)
      toast('Сессия завершена')
      void list.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }
  return (
    <section className="panel">
      <div className="panel-head">
        <div>
          <h2>Активные сессии</h2>
          <p>Устройства, на которых выполнен вход</p>
        </div>
      </div>
      {!list.data ? (
        <Loading />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Устройство</th>
                <th>IP</th>
                <th>Вход</th>
                <th>Активность</th>
                <th className="actions" />
              </tr>
            </thead>
            <tbody>
              {list.data
                .sort((a, b) => Number(b.current) - Number(a.current) || b.lastSeen.localeCompare(a.lastSeen))
                .map((s) => (
                  <tr key={s.id}>
                    <td>
                      <div className="name-cell">
                        <span className="file-icon">
                          <IconDeviceDesktop size={16} />
                        </span>
                        <span className="truncate" title={s.userAgent}>
                          {describeBrowser(s.userAgent)}
                        </span>
                        {s.current && <span className="tag tag-ok">эта сессия</span>}
                      </div>
                    </td>
                    <td className="mono">{s.ip}</td>
                    <td className="muted nowrap">{dateTime(s.createdAt)}</td>
                    <td className="muted nowrap">{relTime(s.lastSeen)}</td>
                    <td className="actions">
                      {!s.current && (
                        <Button size="sm" variant="ghost" icon={<IconLogout size={15} />} onClick={() => revoke(s)}>
                          Завершить
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
