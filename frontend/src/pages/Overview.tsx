import { Link } from 'react-router-dom'
import { IconAlertTriangle, IconListDetails, IconPlus } from '@tabler/icons-react'
import { api } from '../lib/api'
import { RULES, count, fmtBytes, fmtNum, plural, relTime } from '../lib/format'
import { useAsync, useInterval, useNow, useTitle } from '../lib/hooks'
import type { Overview as OverviewData, Ruleset } from '../lib/types'
import { BehaviorTag, Empty, Loading, Notice } from '../ui/core'
import { Temper } from '../ui/temper'
import { EventList } from './parts'

export function Overview() {
  useTitle('Обзор')
  useNow()
  const ov = useAsync(() => api.get<OverviewData>('overview'), [])
  const sets = useAsync(() => api.get<Ruleset[]>('rulesets'), [])
  useInterval(() => {
    void ov.reload()
    void sets.reload()
  }, 20_000)

  if (ov.error) return <Notice tone="error">{ov.error.message}</Notice>
  if (!ov.data || !sets.data) return <Loading />
  const o = ov.data
  const failing = sets.data.filter((r) => r.state.status === 'error' || r.state.error)

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Обзор</h1>
          <p>Состояние наборов правил и хранилища.</p>
        </div>
        <Link to="/rulesets/new" className="btn btn-primary">
          <IconPlus size={17} /> Новый набор
        </Link>
      </div>

      <div className="ledger">
        <div>
          <b>{fmtNum(o.rulesets.total)}</b>
          <span>наборов, {o.rulesets.auto} с автообновлением</span>
        </div>
        <div>
          <b>{fmtNum(o.rulesets.rules)}</b>
          <span>{plural(o.rulesets.rules, RULES)} во всех наборах</span>
        </div>
        <div>
          <b>{fmtNum(o.rulesets.sources)}</b>
          <span>источников</span>
        </div>
        <div>
          <b>{fmtNum(o.storage.objects)}</b>
          <span>файлов в {o.storage.buckets} бакетах</span>
        </div>
        <div>
          <b>{fmtBytes(o.storage.bytes)}</b>
          <span>занято хранилищем</span>
        </div>
      </div>

      {failing.length > 0 && (
        <Notice tone="warn">
          {failing.length === 1 ? 'У набора ' : 'У наборов '}
          {failing.map((r, i) => (
            <span key={r.id}>
              {i > 0 && ', '}
              <Link to={`/rulesets/${r.id}`}>{r.name}</Link>
            </span>
          ))}{' '}
          есть ошибки при последней сборке.
        </Notice>
      )}

      <div className="grid-2">
        <section className="panel">
          <div className="panel-head">
            <h2>Наборы правил</h2>
            <Link to="/rulesets" className="small">
              Все наборы
            </Link>
          </div>
          {sets.data.length === 0 ? (
            <Empty
              icon={<IconListDetails size={30} />}
              title="Наборов пока нет"
              action={
                <Link to="/rulesets/new" className="btn btn-primary">
                  <IconPlus size={17} /> Создать набор
                </Link>
              }
            >
              Добавьте ссылки на списки доменов или IP — панель скачает их, объединит без дублей и выдаст свою ссылку в
              нужном формате.
            </Empty>
          ) : (
            <div className="rs-list">
              {sets.data.slice(0, 8).map((rs) => (
                <Link key={rs.id} to={`/rulesets/${rs.id}`} className="rs-row compact">
                  <div className="truncate">
                    <div className="rs-name truncate">{rs.name}</div>
                    <div className="small muted">{count(rs.state.count, RULES)}</div>
                  </div>
                  <div>
                    <BehaviorTag behavior={rs.state.behavior || rs.behavior} />
                  </div>
                  <div className="rs-fresh">
                    <Temper rs={rs} />
                    <span className="muted">
                      {rs.state.status === 'error' ? (
                        <span className="text-error">
                          <IconAlertTriangle size={13} aria-hidden /> ошибка
                        </span>
                      ) : (
                        relTime(rs.state.lastRunAt)
                      )}
                    </span>
                  </div>
                </Link>
              ))}
            </div>
          )}
        </section>

        <section className="panel">
          <div className="panel-head">
            <div>
              <h2>Журнал изменений</h2>
              <p>Что добавилось и пропало при обновлениях</p>
            </div>
          </div>
          <EventList events={o.events} showName />
        </section>
      </div>
    </div>
  )
}
