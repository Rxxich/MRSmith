import { useState } from 'react'
import { Link } from 'react-router-dom'
import { IconListDetails, IconLock, IconPlus, IconRefresh, IconSearch } from '@tabler/icons-react'
import { api, errorText } from '../lib/api'
import { DOWNLOADS, count, counterTitle, fmtNum, rulesetDownloads } from '../lib/format'
import { useAsync, useInterval, useNow, useTitle } from '../lib/hooks'
import type { Ruleset, RulesetEvent } from '../lib/types'
import { BehaviorTag, Button, Empty, Input, Loading, Notice } from '../ui/core'
import { useOverlay } from '../ui/overlay'
import { Temper } from '../ui/temper'

export function Rulesets() {
  useTitle('Наборы правил')
  useNow()
  const { toast } = useOverlay()
  const list = useAsync(() => api.get<Ruleset[]>('rulesets'), [])
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const anyRunning = !!list.data?.some((r) => r.running || r.state.status === 'pending')
  useInterval(() => void list.reload(), anyRunning ? 1500 : 20_000)

  const refresh = async (rs: Ruleset) => {
    setBusy(rs.id)
    try {
      const res = await api.post<{ ruleset: Ruleset; event: RulesetEvent }>(`rulesets/${rs.id}/refresh`)
      list.setData((all) => all?.map((x) => (x.id === rs.id ? res.ruleset : x)))
      const ev = res.event
      toast(
        ev.kind === 'error'
          ? `${rs.name}: ${ev.message}`
          : ev.kind === 'changed'
            ? `${rs.name}: +${fmtNum(ev.added)} −${fmtNum(ev.removed)}`
            : `${rs.name}: без изменений`,
        ev.kind === 'error' ? 'error' : 'ok',
      )
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy(null)
    }
  }

  const items = (list.data || []).filter((r) => {
    const s = q.trim().toLowerCase()
    return !s || r.name.toLowerCase().includes(s) || r.slug.includes(s)
  })

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Наборы правил</h1>
          <p>Каждый набор собирается из ссылок и файлов, очищается от дублей и публикуется по ссылке вашего домена.</p>
        </div>
        <Link to="/rulesets/new" className="btn btn-primary">
          <IconPlus size={17} /> Новый набор
        </Link>
      </div>

      {list.error && <Notice tone="error">{list.error.message}</Notice>}
      {!list.data ? (
        <Loading />
      ) : list.data.length === 0 ? (
        <section className="panel">
          <Empty
            icon={<IconListDetails size={30} />}
            title="Создайте первый набор"
            action={
              <Link to="/rulesets/new" className="btn btn-primary">
                <IconPlus size={17} /> Новый набор
              </Link>
            }
          >
            Укажите ссылки на списки доменов или IP-адресов в любом формате — YAML, TXT, LIST или MRS. Панель будет
            сама проверять обновления.
          </Empty>
        </section>
      ) : (
        <section className="panel">
          {list.data.length > 6 && (
            <div className="panel-head">
              <div className="search-box">
                <Input placeholder="Найти по названию или ссылке" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Поиск" />
              </div>
              <IconSearch size={16} className="muted" aria-hidden />
            </div>
          )}
          <div className="rs-list">
            <div className="rs-row rs-head" aria-hidden>
              <span>Набор</span>
              <span>Behavior</span>
              <span className="rs-count">Правил</span>
              <span>Свежесть</span>
              <span />
            </div>
            {items.map((rs) => (
              <div key={rs.id} className="rs-row">
                <Link to={`/rulesets/${rs.id}`} className="truncate plain-link">
                  <div className="rs-name truncate">
                    {rs.name} {rs.private && <IconLock size={13} className="muted" aria-label="приватный" />}
                  </div>
                  <div className="small rs-slug truncate mono">
                    /r/{rs.slug}.{'{'}
                    {rs.formats.join(',')}
                    {'}'}
                  </div>
                </Link>
                <div>
                  <BehaviorTag behavior={rs.state.behavior || rs.behavior} />
                </div>
                <div className="rs-count">
                  {rs.state.count ? fmtNum(rs.state.count) : '—'}
                  <div className="small muted">{count(rs.sources.length, ['источник', 'источника', 'источников'])}</div>
                  <RowDownloads rs={rs} />
                </div>
                <div className="rs-fresh">
                  <Temper rs={rs} />
                  <span className="muted">
                    {rs.state.status === 'error' ? (
                      <span className="text-error">{rs.state.error || 'ошибка сборки'}</span>
                    ) : rs.running || rs.state.status === 'pending' ? (
                      'собирается…'
                    ) : (
                      <FreshText rs={rs} />
                    )}
                  </span>
                </div>
                <div className="rs-actions">
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    busy={busy === rs.id}
                    onClick={() => refresh(rs)}
                    title="Обновить сейчас"
                    aria-label={`Обновить ${rs.name} сейчас`}
                    icon={<IconRefresh size={15} />}
                  />
                </div>
              </div>
            ))}
            {items.length === 0 && <div className="panel-body muted">Ничего не нашлось по запросу «{q}».</div>}
          </div>
        </section>
      )}
    </div>
  )
}

function RowDownloads({ rs }: { rs: Ruleset }) {
  const total = rulesetDownloads(rs)
  if (!total.downloads && !total.checks) return null
  return (
    <div className="small muted" title={counterTitle(total)}>
      {count(total.downloads, DOWNLOADS)}
    </div>
  )
}

function FreshText({ rs }: { rs: Ruleset }) {
  if (!rs.state.lastRunAt) return <>ещё не собирался</>
  const warn = rs.state.error ? <span className="text-warn">, с предупреждениями</span> : null
  return (
    <>
      {rs.intervalMin ? <>обновлён {relShort(rs.state.lastRunAt)}</> : <>вручную, {relShort(rs.state.lastRunAt)}</>}
      {warn}
    </>
  )
}

function relShort(iso: string) {
  const min = Math.round((Date.now() - new Date(iso).getTime()) / 60000)
  if (min < 1) return 'только что'
  if (min < 60) return `${min} мин назад`
  const h = Math.round(min / 60)
  if (h < 48) return `${h} ч назад`
  return `${Math.round(h / 24)} дн назад`
}

