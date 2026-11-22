import { type FormEvent, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  IconAdjustments,
  IconArrowBackUp,
  IconBox,
  IconCircleCheck,
  IconCode,
  IconDownload,
  IconLink,
  IconLock,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconTrash,
  IconX,
} from '@tabler/icons-react'
import { useMe } from '../app/auth'
import { api, errorText } from '../lib/api'
import { FORMAT_INFO, RULES, count, counterText, counterTitle, dateTime, droppedText, fmtBytes, fmtNum, providerSnippet, relTime, rulesetDownloads, rulesetLink } from '../lib/format'
import { useAsync, useDebounced, useInterval, useNow, useTitle } from '../lib/hooks'
import type { OutputFormat, Ruleset, RulesetEvent, Source } from '../lib/types'
import { BehaviorTag, Button, CopyButton, CopyField, Input, Loading, Notice, RuleText, StatusDot } from '../ui/core'
import { Modal, useOverlay } from '../ui/overlay'
import { Temper } from '../ui/temper'
import { EventList } from './parts'
import { ExcludeDialog, MODE_LABEL } from './ExcludeDialog'

type Tab = 'entries' | 'sources' | 'overrides' | 'history'

export function RulesetDetail() {
  const { id = '' } = useParams()
  const nav = useNavigate()
  const me = useMe()
  const { toast, confirm } = useOverlay()
  useNow()
  const res = useAsync(() => api.get<Ruleset>(`rulesets/${id}`), [id])
  const rs = res.data
  useTitle(rs?.name || 'Набор')
  const building = !!rs && (rs.running || rs.state.status === 'pending')
  useInterval(() => void res.reload(), building ? 1500 : 30_000)
  const [tab, setTab] = useState<Tab>('entries')
  const [refreshing, setRefreshing] = useState(false)
  const [snippet, setSnippet] = useState<OutputFormat | null>(null)
  const [version, setVersion] = useState(0) // bumps when contents change

  useEffect(() => {
    if (rs && !building) setVersion((v) => v + 1)
  }, [rs?.state.hash, building]) // eslint-disable-line react-hooks/exhaustive-deps

  if (res.error) return <Notice tone="error">{res.error.message}</Notice>
  if (!rs) return <Loading />

  const refresh = async () => {
    setRefreshing(true)
    try {
      const r = await api.post<{ ruleset: Ruleset; event: RulesetEvent }>(`rulesets/${id}/refresh`)
      res.setData(r.ruleset)
      const ev = r.event
      if (ev.kind === 'error') toast(ev.message || 'Сборка не удалась', 'error')
      else if (ev.kind === 'changed') toast(`Обновлено: +${fmtNum(ev.added)} −${fmtNum(ev.removed)}, всего ${fmtNum(ev.total)}`)
      else toast('Источники не изменились')
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setRefreshing(false)
    }
  }

  const remove = async () => {
    const ok = await confirm({
      title: `Удалить «${rs.name}»?`,
      body: 'Ссылки на файлы этого набора перестанут работать у всех клиентов. Источники в хранилище останутся.',
      confirm: 'Удалить набор',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`rulesets/${id}`)
      toast('Набор удалён')
      nav('/rulesets')
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const st = rs.state
  const sourceRules = rs.sources.reduce((n, s) => n + (s.state.rules || 0), 0)

  return (
    <div className="stack">
      <div className="page-head">
        <div className="grow">
          <div className="crumbs">
            <Link to="/rulesets">Наборы правил</Link>
          </div>
          <div className="row-flex">
            <h1>{rs.name}</h1>
            <BehaviorTag behavior={st.behavior || rs.behavior} />
            {rs.private && (
              <span className="tag tag-muted">
                <IconLock size={12} aria-hidden /> по токену
              </span>
            )}
          </div>
          {rs.description && <p>{rs.description}</p>}
        </div>
        <div className="row-flex">
          <Button variant="primary" busy={refreshing || building} onClick={refresh} icon={<IconRefresh size={17} />}>
            {building ? 'Собирается…' : 'Обновить сейчас'}
          </Button>
          <Link to={`/rulesets/${id}/edit`} className="btn btn-secondary">
            <IconAdjustments size={17} /> Настройки
          </Link>
          <Button variant="danger" iconOnly onClick={remove} title="Удалить набор" aria-label="Удалить набор" icon={<IconTrash size={17} />} />
        </div>
      </div>

      <Temper rs={rs} legend />

      {st.status === 'error' && <Notice tone="error">{st.error}. Клиенты продолжают получать последние собранные файлы.</Notice>}
      {st.status === 'ok' && st.error && <Notice tone="warn">{st.error}</Notice>}

      <div className="pipeline">
        <div className="stage">
          <div className="stage-title">
            <IconLink size={15} aria-hidden /> Источники
          </div>
          <div className="stage-list">
            {rs.sources.length === 0 && <span className="muted small">Только ручные правила</span>}
            {rs.sources.map((s) => (
              <div key={s.id} className="src-line" title={s.state.error || undefined}>
                <StatusDot status={building && s.state.status === 'pending' ? 'running' : s.state.status} />
                <span className="truncate mono grow">{sourceLabel(s)}</span>
                <span className="muted small nowrap">{s.state.rules ? fmtNum(s.state.rules) : '—'}</span>
              </div>
            ))}
            {rs.include.length > 0 && (
              <div className="src-line">
                <span className="dot" aria-hidden />
                <span className="grow">Добавлено вручную</span>
                <span className="muted small">{fmtNum(rs.include.length)}</span>
              </div>
            )}
          </div>
        </div>

        <div className="stage">
          <div className="stage-title">
            <IconAdjustments size={15} aria-hidden /> Обработка
          </div>
          <dl className="facts">
            <dt>Из источников</dt>
            <dd>{fmtNum(sourceRules)}</dd>
            {rs.exclude.length > 0 && (
              <>
                <dt>Исключений</dt>
                <dd>{fmtNum(rs.exclude.length)}</dd>
              </>
            )}
            {st.optimized > 0 && (
              <>
                <dt>Убрано перекрытых</dt>
                <dd>{fmtNum(st.optimized)}</dd>
              </>
            )}
            {st.skipped > 0 && (
              <>
                <dt>Не подошло к {st.behavior}</dt>
                <dd className="text-warn" title={droppedText(st.dropped)}>
                  {fmtNum(st.skipped)}
                </dd>
              </>
            )}
            <dt>Итого</dt>
            <dd>
              <b>{fmtNum(st.count)}</b>
            </dd>
          </dl>
          {st.count > 0 && (
            <p className="small muted mt-s">
              {[st.domains && `доменов ${fmtNum(st.domains)}`, st.cidrs && `сетей ${fmtNum(st.cidrs)}`, st.classical && `прочих ${fmtNum(st.classical)}`]
                .filter(Boolean)
                .join(', ')}
              {st.durationMs ? `, сборка ${st.durationMs < 1000 ? `${st.durationMs} мс` : `${(st.durationMs / 1000).toFixed(1)} с`}` : ''}
            </p>
          )}
        </div>

        <div className="stage">
          <div className="stage-title">
            <IconDownload size={15} aria-hidden /> Файлы и ссылки
          </div>
          <div className="stage-list">
            {rs.formats.map((f) => {
              const o = st.outputs?.[f]
              return (
                <div key={f} className="output">
                  <div className="output-head">
                    <span className="output-fmt">{FORMAT_INFO[f].title}</span>
                    {o && !o.error && <span className="muted small">{fmtBytes(o.size)}</span>}
                    {o && !o.error && rs.downloads?.[f] && (
                      <span className="muted small nowrap" title={counterTitle(rs.downloads[f])}>
                        {fmtNum(rs.downloads[f]!.downloads)}
                        <IconDownload size={12} aria-label="скачиваний" className="inline-icon" />
                      </span>
                    )}
                    {o?.verified && (
                      <span className="tag tag-ok" title="Файл декодирован обратно и совпал с набором">
                        <IconCircleCheck size={12} aria-hidden /> проверен
                      </span>
                    )}
                    <span className="grow" />
                    {o && !o.error && (
                      <>
                        <Button size="sm" variant="ghost" iconOnly title="Конфиг для mihomo" aria-label={`Конфиг mihomo для ${f}`} onClick={() => setSnippet(f)} icon={<IconCode size={15} />} />
                        <a className="btn btn-ghost btn-sm btn-icon" href={`api/rulesets/${id}/download/${f}`} title="Скачать" aria-label={`Скачать ${f}`}>
                          <IconDownload size={15} />
                        </a>
                      </>
                    )}
                  </div>
                  {o?.error ? <span className="small text-error">{o.error}</span> : o ? <CopyField value={rulesetLink(me, rs, f)} /> : <span className="small muted">Появится после сборки</span>}
                  {o && !o.error && o.behavior && (
                    <span className="small muted">
                      Только {o.behavior === 'domain' ? count(o.count || 0, ['домен', 'домена', 'доменов']) : count(o.count || 0, ['IP-сеть', 'IP-сети', 'IP-сетей'])}
                      {o.translated ? `, из них ${fmtNum(o.translated)} из DOMAIN-REGEX` : ''}
                      {o.dropped ? `. Не вошли: ${droppedText(o.dropped)}` : ''}. В mihomo — behavior: {o.behavior}.
                    </span>
                  )}
                </div>
              )
            })}
          </div>
          <DownloadsLine rs={rs} />
        </div>
      </div>

      <div>
        <div className="tabs" role="tablist">
          <TabButton id="entries" tab={tab} setTab={setTab} label="Содержимое" n={st.count} />
          <TabButton id="sources" tab={tab} setTab={setTab} label="Источники" n={rs.sources.length} />
          <TabButton id="overrides" tab={tab} setTab={setTab} label="Ручные правки" n={rs.include.length + rs.exclude.length} />
          <TabButton id="history" tab={tab} setTab={setTab} label="История" />
        </div>
        {tab === 'entries' && <Entries rs={rs} version={version} onChanged={res.setData} />}
        {tab === 'sources' && <SourcesTab rs={rs} />}
        {tab === 'overrides' && <Overrides rs={rs} onChanged={res.setData} />}
        {tab === 'history' && <History id={id} version={version} />}
      </div>

      <Modal open={!!snippet} onClose={() => setSnippet(null)} title="Подключение в mihomo" wide>
        {snippet && (
          <div className="stack">
            <p className="dim">Добавьте блок в конфиг клиента и сошлитесь на провайдер в rules, например RULE-SET,{rs.slug},PROXY.</p>
            <div className="spread">
              <span className="muted small">config.yaml</span>
              <CopyButton text={providerSnippet(me, rs, snippet)} label="Скопировать конфиг" />
            </div>
            <pre className="code">{providerSnippet(me, rs, snippet)}</pre>
          </div>
        )}
      </Modal>
    </div>
  )
}

function DownloadsLine({ rs }: { rs: Ruleset }) {
  const total = rulesetDownloads(rs)
  if (!total.downloads && !total.checks) {
    return <p className="small muted mt-s">По ссылкам ещё никто не скачивал.</p>
  }
  return (
    <p className="small muted mt-s" title={counterTitle(total)}>
      {counterText(total)}, последний раз {relTime(total.lastAt)}. <Link to="/settings#downloads">Кто скачивал</Link>
    </p>
  )
}

/** The host or network a rule is about, for the "where else" search. */
function lookupTerm(rule: string) {
  const comma = rule.indexOf(',')
  const value = comma > 0 ? rule.slice(comma + 1).split(',')[0] : rule
  return value.replace(/^(\+\.|\.|\*\.)/, '')
}

function sourceLabel(s: Source) {
  if (s.type === 'object') return `${s.bucket}/${s.key}`
  try {
    const u = new URL(s.url || '')
    return u.host + u.pathname
  } catch {
    return s.url || ''
  }
}

function TabButton({ id, tab, setTab, label, n }: { id: Tab; tab: Tab; setTab: (t: Tab) => void; label: string; n?: number }) {
  return (
    <button type="button" role="tab" aria-selected={tab === id} onClick={() => setTab(id)}>
      {label}
      {n !== undefined && n > 0 && <span className="tab-count">{fmtNum(n)}</span>}
    </button>
  )
}

// ---- contents --------------------------------------------------------------

const PAGE = 100

function Entries({ rs, version, onChanged }: { rs: Ruleset; version: number; onChanged: (r: Ruleset) => void }) {
  const { toast } = useOverlay()
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [offset, setOffset] = useState(0)
  const [add, setAdd] = useState('')
  const [busy, setBusy] = useState(false)
  const [excluding, setExcluding] = useState<string | null>(null)
  useEffect(() => setOffset(0), [dq])
  const data = useAsync(
    () => api.get<{ total: number; items: string[] }>(`rulesets/${rs.id}/entries?q=${encodeURIComponent(dq)}&offset=${offset}&limit=${PAGE}`),
    [rs.id, dq, offset, version],
  )

  const edit = async (body: { add?: string[] }, done: string) => {
    setBusy(true)
    try {
      onChanged(await api.post<Ruleset>(`rulesets/${rs.id}/entries`, body))
      toast(done)
      await data.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy(false)
    }
  }

  const excluded = async (updated: Ruleset) => {
    setExcluding(null)
    onChanged(updated)
    await data.reload()
  }

  const submitAdd = async (e: FormEvent) => {
    e.preventDefault()
    const items = add.split(/\s+/).filter(Boolean)
    if (!items.length) return
    await edit({ add: items }, items.length === 1 ? `${items[0]} добавлено` : `Добавлено ${count(items.length, RULES)}`)
    setAdd('')
  }

  return (
    <div className="stack">
      <div className="spread">
        <form className="input-group add-form" onSubmit={submitAdd}>
          <Input
            mono
            value={add}
            onChange={(e) => setAdd(e.target.value)}
            placeholder="+.example.com, 1.2.3.0/24 или DOMAIN-KEYWORD,ads"
            aria-label="Добавить правило"
          />
          <Button type="submit" variant="secondary" busy={busy} icon={<IconPlus size={16} />} disabled={!add.trim()}>
            Добавить
          </Button>
        </form>
        <div className="input-group search-box">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Поиск по правилам" aria-label="Поиск по правилам" />
          <IconSearch size={16} className="muted search-icon" aria-hidden />
        </div>
      </div>
      {!data.data ? (
        <Loading />
      ) : data.data.total === 0 ? (
        <p className="muted">{dq ? `Ничего не найдено по «${dq}».` : 'Набор пока пуст.'}</p>
      ) : (
        <>
          <div className="entries">
            {data.data.items.map((e) => (
              <div key={e} className="entry">
                <span className="entry-text">
                  <RuleText rule={e} />
                </span>
                <Link className="btn btn-ghost btn-sm btn-icon del" to={`/audit?q=${encodeURIComponent(lookupTerm(e))}`} title="Где ещё встречается" aria-label={`Где ещё встречается ${e}`}>
                  <IconSearch size={14} />
                </Link>
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  className="del"
                  disabled={busy}
                  onClick={() => setExcluding(e)}
                  title="Исключить из набора"
                  aria-label={`Исключить ${e}`}
                  icon={<IconX size={14} />}
                />
              </div>
            ))}
          </div>
          <div className="pager">
            <span>
              {fmtNum(offset + 1)}–{fmtNum(Math.min(offset + PAGE, data.data.total))} из {fmtNum(data.data.total)}
            </span>
            <div className="row-flex">
              <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                Назад
              </Button>
              <Button size="sm" disabled={offset + PAGE >= data.data.total} onClick={() => setOffset(offset + PAGE)}>
                Дальше
              </Button>
            </div>
          </div>
        </>
      )}
      <ExcludeDialog rs={rs} rule={excluding} onClose={() => setExcluding(null)} onDone={excluded} />
    </div>
  )
}

// ---- sources ---------------------------------------------------------------

function SourcesTab({ rs }: { rs: Ruleset }) {
  if (rs.sources.length === 0) return <p className="muted">У набора нет источников — только ручные правила.</p>
  return (
    <div className="stack">
      {rs.sources.map((s, i) => (
        <section key={s.id} className="panel">
          <div className="panel-head">
            <div className="row-flex grow">
              <StatusDot status={s.state.status} />
              {s.type === 'object' ? <IconBox size={16} className="muted" aria-hidden /> : <IconLink size={16} className="muted" aria-hidden />}
              <span className="mono truncate grow">{s.type === 'url' ? s.url : `${s.bucket}/${s.key}`}</span>
            </div>
            <span className="muted small">Источник {i + 1}</span>
          </div>
          <div className="panel-body stack-s">
            {s.state.error && <Notice tone="error">{s.state.error}</Notice>}
            <dl className="facts narrow">
              <dt>Правил</dt>
              <dd>{fmtNum(s.state.rules)}</dd>
              <dt>Формат</dt>
              <dd>{s.state.detectedFormat || '—'}</dd>
              <dt>Содержимое</dt>
              <dd>{s.state.detectedBehavior ? <BehaviorTag behavior={s.state.detectedBehavior} /> : '—'}</dd>
              <dt>Размер</dt>
              <dd>{s.state.size ? fmtBytes(s.state.size) : '—'}</dd>
              <dt>Проверен</dt>
              <dd title={dateTime(s.state.checkedAt)}>{relTime(s.state.checkedAt)}</dd>
              <dt>Последнее изменение</dt>
              <dd title={dateTime(s.state.changedAt)}>{relTime(s.state.changedAt)}</dd>
              {s.state.cacheKey && (
                <>
                  <dt>Скачанная копия</dt>
                  <dd>
                    <Link to={`/storage/downloads?p=${encodeURIComponent(s.state.cacheKey.split('/')[0] + '/')}`} className="mono">
                      downloads/{s.state.cacheKey}
                    </Link>
                  </dd>
                </>
              )}
              {s.bareAsSuffix && (
                <>
                  <dt>Голые домены</dt>
                  <dd>с поддоменами</dd>
                </>
              )}
            </dl>
            {s.state.invalid > 0 && (
              <details>
                <summary className="text-warn small">Не распознано строк: {fmtNum(s.state.invalid)}</summary>
                <pre className="code mt-s">
                  {(s.state.invalidSample || []).join('\n')}
                </pre>
              </details>
            )}
          </div>
        </section>
      ))}
    </div>
  )
}

// ---- overrides ----------------------------------------------------------

function Overrides({ rs, onChanged }: { rs: Ruleset; onChanged: (r: Ruleset) => void }) {
  const { toast } = useOverlay()
  const [busy, setBusy] = useState(false)
  const [typed, setTyped] = useState('')
  const [excluding, setExcluding] = useState<string | null>(null)
  const clear = async (include: string[], exclude: string[]) => {
    setBusy(true)
    try {
      onChanged(await api.post<Ruleset>(`rulesets/${rs.id}/overrides/clear`, { include, exclude }))
      toast('Правка отменена')
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy(false)
    }
  }
  const still = rs.state.still || []
  const stillBy = new Map(still.map((s) => [s.rule, s.by]))
  return (
    <div className="stack">
      <form
        className="input-group add-form"
        onSubmit={(e) => {
          e.preventDefault()
          if (typed.trim()) setExcluding(typed.trim())
        }}
      >
        <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Что исключить: link.example.com, +.example.com, 10.0.0.0/8" aria-label="Правило для исключения" />
        <Button type="submit" variant="secondary" disabled={!typed.trim()} icon={<IconX size={16} />}>
          Исключить…
        </Button>
      </form>
      <div className="grid-2">
        <section className="panel">
          <div className="panel-head">
            <h2>Добавлено вручную</h2>
            <span className="muted small">{fmtNum(rs.include.length)}</span>
          </div>
          {rs.include.length === 0 ? (
            <p className="panel-body muted">Правила, добавленные во вкладке «Содержимое», появятся здесь.</p>
          ) : (
            <div className="entries flat">
              {rs.include.slice(0, 500).map((e) => (
                <div key={e} className="entry">
                  <span className="entry-text">
                    <RuleText rule={e} />
                  </span>
                  <Button size="sm" variant="ghost" iconOnly className="del" disabled={busy} onClick={() => clear([e], [])} title="Отменить правку" aria-label={`Отменить правку ${e}`} icon={<IconArrowBackUp size={14} />} />
                </div>
              ))}
            </div>
          )}
        </section>
        <section className="panel">
          <div className="panel-head">
            <h2>Исключено</h2>
            <span className="muted small">{fmtNum(rs.exclude.length)}</span>
          </div>
          {rs.exclude.length === 0 ? (
            <p className="panel-body muted">Исключённые правила появятся здесь. Они применяются при каждом обновлении источников.</p>
          ) : (
            <div className="entries flat">
              {rs.exclude.slice(0, 500).map((x) => (
                <div key={x.rule} className="entry exclusion">
                  <span className="entry-text">
                    <RuleText rule={x.rule} />
                    <span className="mode-tag">{MODE_LABEL[x.mode]}</span>
                    {stillBy.has(x.rule) && (
                      <span className="still" title="Адреса по-прежнему совпадают через более широкое правило">
                        совпадает через {stillBy.get(x.rule)}
                      </span>
                    )}
                  </span>
                  <span className="exclusion-actions">
                    <Button size="sm" variant="ghost" iconOnly className="del" disabled={busy} onClick={() => setExcluding(x.rule)} title="Изменить режим" aria-label={`Изменить режим для ${x.rule}`} icon={<IconAdjustments size={14} />} />
                    <Button size="sm" variant="ghost" iconOnly className="del" disabled={busy} onClick={() => clear([], [x.rule])} title="Вернуть в набор" aria-label={`Вернуть ${x.rule}`} icon={<IconArrowBackUp size={14} />} />
                  </span>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>
      <ExcludeDialog
        rs={rs}
        rule={excluding}
        onClose={() => setExcluding(null)}
        onDone={(updated) => {
          setExcluding(null)
          setTyped('')
          onChanged(updated)
        }}
      />
    </div>
  )
}

function History({ id, version }: { id: string; version: number }) {
  const evs = useAsync(() => api.get<RulesetEvent[]>(`rulesets/${id}/events`), [id, version])
  if (!evs.data) return <Loading />
  return (
    <section className="panel">
      <EventList events={evs.data} />
    </section>
  )
}
