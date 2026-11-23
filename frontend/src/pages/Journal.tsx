import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { IconSearch, IconTrash } from '@tabler/icons-react'
import { api, errorText } from '../lib/api'
import { CHECKS, DOWNLOADS, count, dateTime, describeAgent, fmtBytes, fmtNum, relTime } from '../lib/format'
import { useAsync, useDebounced } from '../lib/hooks'
import type { DownloadHit, DownloadJournal, DownloadOutcome } from '../lib/types'
import { Button, Input, Loading, Notice, Segmented } from '../ui/core'
import { useOverlay } from '../ui/overlay'

type View = 'clients' | 'recent'
type OutcomeFilter = '' | 'ok' | 'not-modified' | 'failed'

const PAGE = 50
const FAILED: [string, string, string] = ['не прошёл', 'не прошли', 'не прошли']

const OUTCOME: Record<DownloadOutcome, { label: string; cls: string; hint: string }> = {
  ok: { label: 'скачал', cls: 'tag tag-ok', hint: 'Панель отдала файл целиком' },
  'not-modified': {
    label: 'не изменился',
    cls: 'tag tag-muted',
    hint: 'Клиент спросил, не изменился ли файл. Его копия актуальна, поэтому файл заново не передавался',
  },
  aborted: { label: 'оборвалась', cls: 'tag tag-warn', hint: 'Передача прервалась: соединение закрылось раньше, чем ушёл весь файл' },
  'bad-token': { label: 'неверный токен', cls: 'tag tag-error', hint: 'Набор приватный, а в ссылке нет токена или он неверный. Клиент получил 404' },
  missing: { label: 'нет файла', cls: 'tag tag-error', hint: 'Такого файла нет: набор ещё не собран, этот формат не публикуется или файл удалён. Клиент получил 404' },
  private: { label: 'бакет закрыт', cls: 'tag tag-warn', hint: 'Бакет не публичный, по прямой ссылке файлы из него не отдаются. Клиент получил 404' },
  error: { label: 'ошибка сервера', cls: 'tag tag-error', hint: 'Панель не смогла отдать файл, подробности в логах контейнера' },
}

/** Links a file name from the journal to where it lives in the panel. */
function FileRef({ hit }: { hit: DownloadHit }) {
  if (hit.rulesetId) return <Link to={`/rulesets/${hit.rulesetId}`}>{hit.name}</Link>
  if (hit.bucket && hit.key) {
    const dir = hit.key.includes('/') ? hit.key.slice(0, hit.key.lastIndexOf('/') + 1) : ''
    return <Link to={`/storage/${hit.bucket}${dir ? `?p=${encodeURIComponent(dir)}` : ''}`}>{hit.name}</Link>
  }
  return <>{hit.name}</>
}

function Outcome({ hit }: { hit: DownloadHit }) {
  const o = OUTCOME[hit.outcome] || OUTCOME.ok
  const size =
    hit.outcome === 'aborted' && hit.size
      ? `${fmtBytes(hit.bytes || 0)} из ${fmtBytes(hit.size)}`
      : hit.outcome === 'ok' && hit.size
        ? fmtBytes(hit.size)
        : ''
  return (
    <span className="outcome" title={o.hint}>
      <span className={o.cls}>{o.label}</span>
      {size && <span className="small muted">{size}</span>}
    </span>
  )
}

export function Journal() {
  const { toast, confirm } = useOverlay()
  const [view, setView] = useState<View>('clients')
  const [q, setQ] = useState('')
  const dq = useDebounced(q.trim())
  const [outcome, setOutcome] = useState<OutcomeFilter>('')
  const [offset, setOffset] = useState(0)
  useEffect(() => setOffset(0), [view, dq, outcome])
  const journal = useAsync(
    () =>
      api.get<DownloadJournal>(
        `downloads?view=${view}&q=${encodeURIComponent(dq)}&outcome=${outcome}&offset=${offset}&limit=${PAGE}`,
      ),
    [view, dq, outcome, offset],
  )
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

  /** Shows every request from one address. */
  const showIP = (ip: string) => {
    setQ(ip)
    setOutcome('')
    setView('recent')
    ref.current?.scrollIntoView({ block: 'start' })
  }

  const d = journal.data
  const t = d?.totals
  const empty = t && t.hits === 0
  const filtered = !!dq || !!outcome
  return (
    <section className="panel" id="downloads" ref={ref}>
      <div className="panel-head">
        <div>
          <h2>Кто скачивал</h2>
          <p>
            {t && t.hits > 0
              ? `${count(t.hits, ['запрос', 'запроса', 'запросов'])} по публичным ссылкам с ${dateTime(t.since)}`
              : 'Запросы к ссылкам /r/ и /f/ от клиентов mihomo, браузеров и скриптов'}
          </p>
        </div>
        {t && t.hits > 0 && (
          <div className="row-flex">
            <Segmented
              label="Вид журнала"
              value={view}
              onChange={setView}
              options={[
                { value: 'clients', label: `Клиенты ${fmtNum(t.clients)}` },
                { value: 'recent', label: 'Все запросы' },
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
      ) : !d || !t ? (
        <Loading />
      ) : empty ? (
        <div className="panel-body muted">Пока никто не скачивал файлы по ссылкам. Запросы из самой панели сюда не попадают.</div>
      ) : (
        <>
          <div className="panel-body stack-s journal-tools">
            <div className="spread">
              <div className="search-box journal-search">
                <Input
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  placeholder="IP, программа или файл"
                  aria-label="Поиск по журналу"
                />
                <IconSearch size={16} className="muted search-icon" aria-hidden />
              </div>
              <Segmented
                label="Результат"
                value={outcome}
                onChange={setOutcome}
                options={[
                  { value: '', label: 'Все' },
                  { value: 'ok', label: 'Скачал' },
                  { value: 'not-modified', label: 'Не изменился' },
                  { value: 'failed', label: `Не прошли ${fmtNum(t.failed)}` },
                ]}
              />
            </div>
            <p className="small muted">
              Скачал: панель отдала файл целиком. Не изменился: клиент спросил, не обновился ли файл, и его копия оказалась актуальной, поэтому файл
              заново не передавался. Так mihomo сверяет копию по interval из rule-providers, и это считается проверкой обновления. Не прошли:
              загрузка оборвалась, неверный токен, нет файла или бакет закрыт.
            </p>
          </div>
          {d.total === 0 ? (
            <div className="panel-body muted journal-empty">
              Ничего не найдено{dq ? ` по «${dq}»` : ''}.{' '}
              {filtered && (
                <button
                  type="button"
                  className="link-btn"
                  onClick={() => {
                    setQ('')
                    setOutcome('')
                  }}
                >
                  Сбросить поиск
                </button>
              )}
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
                    <th className="num wrap-head" title="Клиент спросил, не изменился ли файл, и получил ответ «не изменился»">
                      Проверок обновления
                    </th>
                    <th className="num">Не прошли</th>
                    <th>Последний раз</th>
                  </tr>
                </thead>
                <tbody>
                  {d.clients.map((c) => (
                    <tr key={c.ip + '\u0000' + c.userAgent}>
                      <td>
                        <span className="truncate agent" title={c.userAgent || 'Клиент не прислал User-Agent'}>
                          {describeAgent(c.userAgent)}
                        </span>
                      </td>
                      <td className="mono nowrap">
                        <button type="button" className="link-btn mono" title="Показать все запросы с этого адреса" onClick={() => showIP(c.ip)}>
                          {c.ip}
                        </button>
                      </td>
                      <td>
                        <span className="truncate file-names mono-soft" title={c.files.join('\n')}>
                          {c.files.join(', ')}
                          {c.moreFiles > 0 && ` и ещё ${fmtNum(c.moreFiles)}`}
                        </span>
                      </td>
                      <td className="num">{c.downloads ? fmtNum(c.downloads) : '—'}</td>
                      <td className="num muted">{c.checks ? fmtNum(c.checks) : '—'}</td>
                      <td className="num muted">{c.failed ? <span className="text-error">{fmtNum(c.failed)}</span> : '—'}</td>
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
                    <th>Результат</th>
                  </tr>
                </thead>
                <tbody>
                  {d.recent.map((h, i) => (
                    <tr key={h.time + i}>
                      <td className="muted nowrap">{dateTime(h.time)}</td>
                      <td className="mono-soft">
                        <span className="truncate file-names">
                          <FileRef hit={h} />
                        </span>
                      </td>
                      <td>
                        <span className="truncate agent" title={h.userAgent || 'Клиент не прислал User-Agent'}>
                          {describeAgent(h.userAgent)}
                        </span>
                      </td>
                      <td className="mono nowrap">
                        <button type="button" className="link-btn mono" title="Показать все запросы с этого адреса" onClick={() => showIP(h.ip)}>
                          {h.ip}
                        </button>
                      </td>
                      <td className="nowrap">
                        <Outcome hit={h} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div className="panel-foot journal-foot">
            {d.total > PAGE && (
              <div className="pager journal-pager">
                <span>
                  {fmtNum(offset + 1)}–{fmtNum(Math.min(offset + PAGE, d.total))} из {fmtNum(d.total)}
                  {view === 'clients' ? ' клиентов' : ' запросов'}
                </span>
                <div className="row-flex">
                  <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                    Назад
                  </Button>
                  <Button size="sm" disabled={offset + PAGE >= d.total} onClick={() => setOffset(offset + PAGE)}>
                    Дальше
                  </Button>
                </div>
              </div>
            )}
            <p className="small muted">
              Журнал хранит последние {fmtNum(t.keep)} запросов. Всего за это время {count(t.downloads, DOWNLOADS)}, {count(t.checks, CHECKS)}
              {t.failed > 0 && ` и ${fmtNum(t.failed)} ${t.failed % 10 === 1 && t.failed % 100 !== 11 ? FAILED[0] : FAILED[1]}`}.
            </p>
          </div>
        </>
      )}
    </section>
  )
}
