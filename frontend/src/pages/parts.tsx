import { Link } from 'react-router-dom'
import { IconHistory } from '@tabler/icons-react'
import { dateTime, fmtNum, relTime } from '../lib/format'
import type { RulesetEvent } from '../lib/types'
import { Empty, RuleText } from '../ui/core'

const TRIGGER: Record<string, string> = {
  schedule: 'по расписанию',
  manual: 'вручную',
  edit: 'после правки',
  create: 'первая сборка',
}

export function EventList({ events, showName }: { events: RulesetEvent[]; showName?: boolean }) {
  if (events.length === 0) {
    return (
      <Empty icon={<IconHistory size={28} />} title="Изменений пока нет">
        Здесь появятся обновления наборов: сколько правил добавилось и пропало.
      </Empty>
    )
  }
  return (
    <div className="events">
      {events.map((ev) => (
        <div key={ev.id} className="event">
          <span className={`dot ${ev.kind === 'error' ? 'dot-error' : ev.kind === 'changed' ? 'dot-ok' : ''}`} aria-hidden />
          <div className="grow">
            <div className="event-title">
              {showName ? <Link to={`/rulesets/${ev.rulesetId}`}>{ev.rulesetName}</Link> : null}
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
                {ev.sampleAdded?.slice(0, 6).map((r) => (
                  <span key={'+' + r} className="plus">
                    + <RuleText rule={r} />
                  </span>
                ))}
                {ev.sampleRemoved?.slice(0, 6).map((r) => (
                  <span key={'-' + r} className="minus">
                    − <RuleText rule={r} />
                  </span>
                ))}
              </div>
            ) : null}
          </div>
          {ev.kind !== 'error' && (
            <div className="delta">
              <span className="plus">+{fmtNum(ev.added)}</span>
              <span className="minus">−{fmtNum(ev.removed)}</span>
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
