import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { IconHistory, IconListDetails, IconSearch } from '@tabler/icons-react'
import { api } from '../lib/api'
import { RULES, count, dateTime, fmtNum, relTime } from '../lib/format'
import { useAsync, useDebounced } from '../lib/hooks'
import type { DiffPage, RulesetEvent } from '../lib/types'
import { Button, Empty, Input, Loading, Notice, RuleText, Segmented } from '../ui/core'
import { Modal } from '../ui/overlay'

const TRIGGER: Record<string, string> = {
  schedule: 'по расписанию',
  manual: 'вручную',
  edit: 'после правки',
  create: 'первая сборка',
  restore: 'после восстановления',
}

const SHOWN = 6

export function EventList({ events, showName }: { events: RulesetEvent[]; showName?: boolean }) {
  const [open, setOpen] = useState<RulesetEvent | null>(null)
  if (events.length === 0) {
    return (
      <Empty icon={<IconHistory size={28} />} title="Изменений пока нет">
        Здесь появятся обновления наборов: сколько правил добавилось и пропало.
      </Empty>
    )
  }
  return (
    <div className="events">
      {events.map((ev) => {
        const more = ev.hasDiff && ev.added + ev.removed > Math.min(ev.added, SHOWN) + Math.min(ev.removed, SHOWN)
        return (
          <div key={ev.id} className={ev.hasDiff ? 'event has-diff' : 'event'} onClick={ev.hasDiff ? () => setOpen(ev) : undefined}>
            <span className={`dot ${ev.kind === 'error' ? 'dot-error' : ev.kind === 'changed' ? 'dot-ok' : ''}`} aria-hidden />
            <div className="grow">
              <div className="event-title">
                {showName ? (
                  <Link to={`/rulesets/${ev.rulesetId}`} onClick={(e) => e.stopPropagation()}>
                    {ev.rulesetName}
                  </Link>
                ) : null}
                {showName ? ' — ' : ''}
                {ev.kind === 'error' ? 'сборка не удалась' : ev.kind === 'changed' ? 'обновлён' : 'без изменений'}
              </div>
              <div className="event-meta" title={dateTime(ev.time)}>
                {relTime(ev.time)}, {TRIGGER[ev.trigger] || ev.trigger}
                {ev.kind !== 'error' && `, всего ${fmtNum(ev.total)}`}
              </div>
              {ev.message && <div className={ev.kind === 'error' ? 'small text-error' : 'small muted'}>{ev.message}</div>}
              {(ev.sampleAdded?.length || ev.sampleRemoved?.length) && ev.trigger !== 'create' ? (
                <div className="sample">
                  {ev.sampleAdded?.slice(0, SHOWN).map((r) => (
                    <span key={'+' + r} className="plus">
                      + <RuleText rule={r} />
                    </span>
                  ))}
                  {ev.sampleRemoved?.slice(0, SHOWN).map((r) => (
                    <span key={'-' + r} className="minus">
                      − <RuleText rule={r} />
                    </span>
                  ))}
                </div>
              ) : null}
            </div>
            {ev.kind !== 'error' && (
              <div className="event-side">
                <div className="delta">
                  <span className="plus">+{fmtNum(ev.added)}</span>
                  <span className="minus">−{fmtNum(ev.removed)}</span>
                </div>
                {ev.hasDiff && (
                  <button
                    type="button"
                    className="link-btn small"
                    onClick={(e) => {
                      e.stopPropagation()
                      setOpen(ev)
                    }}
                  >
                    {more ? 'Весь список' : 'Открыть списком'}
                  </button>
                )}
              </div>
            )}
          </div>
        )
      })}
      <Modal open={!!open} onClose={() => setOpen(null)} title={open ? `${open.rulesetName}: изменения от ${dateTime(open.time)}` : ''} wide>
        {open && <DiffView ev={open} />}
      </Modal>
    </div>
  )
}

const PAGE = 100

/** The full list of what one build added or removed, searchable and paged. */
function DiffView({ ev }: { ev: RulesetEvent }) {
  const [kind, setKind] = useState<'added' | 'removed'>(ev.added > 0 || ev.removed === 0 ? 'added' : 'removed')
  const [q, setQ] = useState('')
  const dq = useDebounced(q.trim())
  const [offset, setOffset] = useState(0)
  useEffect(() => setOffset(0), [kind, dq])
  const page = useAsync(
    () =>
      api.get<DiffPage>(
        `rulesets/${ev.rulesetId}/events/${ev.id}/diff?kind=${kind}&q=${encodeURIComponent(dq)}&offset=${offset}&limit=${PAGE}`,
      ),
    [ev.rulesetId, ev.id, kind, dq, offset],
  )
  const d = page.data
  return (
    <div className="stack diff-view">
      <div className="spread">
        <Segmented
          label="Что показать"
          value={kind}
          onChange={setKind}
          options={[
            { value: 'added', label: `Добавлены ${fmtNum(ev.added)}` },
            { value: 'removed', label: `Убраны ${fmtNum(ev.removed)}` },
          ]}
        />
        <div className="search-box">
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Найти правило" aria-label="Найти правило" />
          <IconSearch size={16} className="muted search-icon" aria-hidden />
        </div>
      </div>
      {page.error ? (
        <Notice tone="error">{page.error.message}</Notice>
      ) : !d ? (
        <Loading />
      ) : d.total === 0 ? (
        <Empty icon={<IconListDetails size={26} />} title={dq ? 'Ничего не найдено' : kind === 'added' ? 'Ничего не добавилось' : 'Ничего не убрано'}>
          {dq ? `Среди ${kind === 'added' ? 'добавленных' : 'убранных'} нет правил с «${dq}».` : undefined}
        </Empty>
      ) : (
        <>
          <ol className="diff-list" start={offset + 1}>
            {d.items.map((r) => (
              <li key={r} className={kind === 'added' ? 'plus' : 'minus'}>
                <span aria-hidden>{kind === 'added' ? '+ ' : '− '}</span>
                <RuleText rule={r} />
              </li>
            ))}
          </ol>
          <div className="pager">
            <span>
              {fmtNum(offset + 1)}–{fmtNum(Math.min(offset + PAGE, d.total))} из {count(d.total, RULES)}
            </span>
            {d.total > PAGE && (
              <div className="row-flex">
                <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                  Назад
                </Button>
                <Button size="sm" disabled={offset + PAGE >= d.total} onClick={() => setOffset(offset + PAGE)}>
                  Дальше
                </Button>
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}
