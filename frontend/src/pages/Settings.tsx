import { type FormEvent, useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { IconDeviceDesktop, IconKey, IconLogout, IconSend, IconTrash } from '@tabler/icons-react'
import { useAuth, useMe } from '../app/auth'
import { api, errorText } from '../lib/api'
import { CHECKS, DOWNLOADS, count, dateTime, describeAgent, describeBrowser, fmtBytes, fmtNum, publicBase, relTime } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { DownloadJournal, Integrations, ProxyInfo, SessionInfo } from '../lib/types'
import { Button, Field, Input, Loading, Notice, Segmented } from '../ui/core'
import { useOverlay } from '../ui/overlay'

export function Settings() {
  useTitle('Настройки')
  const me = useMe()
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Настройки</h1>
          <p>Вход в панель, связь с GitHub и Telegram, активные сессии и кто скачивал файлы.</p>
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
      <Sessions />
      <Downloads />
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

type JournalView = 'clients' | 'recent'

/** Links a file name from the journal to where it lives in the panel. */
function FileRef({ name, rulesetId, bucket, keyName }: { name: string; rulesetId?: string; bucket?: string; keyName?: string }) {
  if (rulesetId) return <Link to={`/rulesets/${rulesetId}`}>{name}</Link>
  if (bucket && keyName) {
    const dir = keyName.includes('/') ? keyName.slice(0, keyName.lastIndexOf('/') + 1) : ''
    return <Link to={`/storage/${bucket}${dir ? `?p=${encodeURIComponent(dir)}` : ''}`}>{name}</Link>
  }
  return <>{name}</>
}

function Downloads() {
  const { toast, confirm } = useOverlay()
  const journal = useAsync(() => api.get<DownloadJournal>('downloads'), [])
  const [view, setView] = useState<JournalView>('clients')
  const ref = useRef<HTMLElement>(null)
  const { hash } = useLocation()
  const loaded = !!journal.data
  useEffect(() => {
    if (hash === '#downloads' && loaded) ref.current?.scrollIntoView({ block: 'start' })
  }, [hash, loaded])

  const clear = async () => {
    const ok = await confirm({
      title: 'Очистить журнал скачиваний?',
      body: 'Список клиентов и запросов начнётся заново. Счётчики у наборов и файлов останутся.',
      confirm: 'Очистить журнал',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del('downloads')
      toast('Журнал очищен')
      void journal.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const d = journal.data
  const s = d?.summary
  return (
    <section className="panel" id="downloads" ref={ref}>
      <div className="panel-head">
        <div>
          <h2>Кто скачивал</h2>
          <p>
            {s && s.hits > 0
              ? `${count(s.hits, ['запрос', 'запроса', 'запросов'])} по публичным ссылкам с ${dateTime(s.since)}`
              : 'Запросы к ссылкам /r/ и /f/ от клиентов mihomo, браузеров и скриптов'}
          </p>
        </div>
        {s && s.hits > 0 && (
          <div className="row-flex">
            <Segmented
              label="Вид журнала"
              value={view}
              onChange={setView}
              options={[
                { value: 'clients', label: `Клиенты ${fmtNum(s.clients.length)}` },
                { value: 'recent', label: 'Последние запросы' },
              ]}
            />
            <Button size="sm" variant="ghost" onClick={clear} icon={<IconTrash size={15} />}>
              Очистить
            </Button>
          </div>
        )}
      </div>
      {journal.error ? (
        <div className="panel-body">
          <Notice tone="error">{journal.error.message}</Notice>
        </div>
      ) : !d || !s ? (
        <Loading />
      ) : s.hits === 0 ? (
        <div className="panel-body muted">
          Пока никто не скачивал файлы по ссылкам. Запросы из самой панели сюда не попадают.
        </div>
      ) : view === 'clients' ? (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Клиент</th>
                <th>IP</th>
                <th>Файлы</th>
                <th className="num">Скачиваний</th>
                <th className="num" title="Клиент спросил, нет ли новой версии, и получил ответ «не изменилось»">
                  Проверок
                </th>
                <th>Последний раз</th>
              </tr>
            </thead>
            <tbody>
              {s.clients.map((c) => (
                <tr key={c.ip + '\u0000' + c.userAgent}>
                  <td>
                    <span className="truncate agent" title={c.userAgent || 'Клиент не прислал User-Agent'}>
                      {describeAgent(c.userAgent)}
                    </span>
                  </td>
                  <td className="mono nowrap">{c.ip}</td>
                  <td>
                    <span className="truncate file-names mono-soft" title={c.files.join('\n')}>
                      {c.files.join(', ')}
                      {c.moreFiles > 0 && ` и ещё ${fmtNum(c.moreFiles)}`}
                    </span>
                  </td>
                  <td className="num">{fmtNum(c.downloads)}</td>
                  <td className="num muted">{c.checks ? fmtNum(c.checks) : '—'}</td>
                  <td className="muted nowrap" title={`Первый запрос ${dateTime(c.firstAt)}`}>
                    {relTime(c.lastAt)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>Время</th>
                <th>Файл</th>
                <th>Клиент</th>
                <th>IP</th>
                <th>Ответ</th>
              </tr>
            </thead>
            <tbody>
              {d.recent.map((h, i) => (
                <tr key={h.time + i}>
                  <td className="muted nowrap">{dateTime(h.time)}</td>
                  <td className="mono-soft">
                    <span className="truncate file-names">
                      <FileRef name={h.name} rulesetId={h.rulesetId} bucket={h.bucket} keyName={h.key} />
                    </span>
                  </td>
                  <td>
                    <span className="truncate agent" title={h.userAgent || 'Клиент не прислал User-Agent'}>
                      {describeAgent(h.userAgent)}
                    </span>
                  </td>
                  <td className="mono nowrap">{h.ip}</td>
                  <td className="muted nowrap">{h.notModified ? 'не изменился' : 'скачал'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {s && s.hits > 0 && (
        <div className="panel-foot journal-foot">
          <p className="small muted">
            Журнал хранит последние {fmtNum(s.keep)} запросов. Всего за это время {count(s.clients.reduce((n, c) => n + c.downloads, 0), DOWNLOADS)} и{' '}
            {count(s.clients.reduce((n, c) => n + c.checks, 0), CHECKS)}.
          </p>
        </div>
      )}
    </section>
  )
}
