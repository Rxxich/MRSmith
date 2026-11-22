import type { Ruleset } from '../lib/types'
import { fmtInterval, relTime } from '../lib/format'

/** How far a rule set has "cooled" since its last build: 0 just built, 1 due. */
export function cooling(rs: Ruleset): number | null {
  if (!rs.intervalMin || !rs.state.lastRunAt) return null
  const elapsed = Date.now() - new Date(rs.state.lastRunAt).getTime()
  return Math.min(1, Math.max(0, elapsed / (rs.intervalMin * 60_000)))
}

const STOPS = ['var(--straw)', 'var(--bronze)', 'var(--violet)', 'var(--blue)']

export function temperColor(f: number) {
  return STOPS[Math.min(STOPS.length - 1, Math.floor(f * STOPS.length))]
}

interface TemperProps {
  rs: Ruleset
  legend?: boolean
}

/** The temper strip: the gradient steel passes through as it heats, revealed
 *  up to the share of the update interval that has elapsed. */
export function Temper({ rs, legend }: TemperProps) {
  const f = cooling(rs)
  const building = rs.running || rs.state.status === 'pending'
  let cls = 'temper'
  if (building) cls += ' is-hot'
  else if (rs.state.status === 'error') cls += ' is-error'
  else if (f === null) cls += ' is-manual'

  const label = building
    ? 'Собирается…'
    : rs.state.status === 'error'
      ? 'Последняя сборка с ошибкой'
      : f === null
        ? `Обновлён ${relTime(rs.state.lastRunAt)}, обновление вручную`
        : `Обновлён ${relTime(rs.state.lastRunAt)}, следующее ${relTime(rs.state.nextRunAt)}`

  return (
    <div>
      <div className={cls} role="img" aria-label={label} title={label}>
        {!building && f !== null && rs.state.status !== 'error' && (
          <span className="temper-cover" style={{ width: `${(1 - Math.max(f, 0.03)) * 100}%` }} />
        )}
      </div>
      {legend && (
        <div className="temper-legend">
          <span>{building ? 'Собирается…' : `Обновлён ${relTime(rs.state.lastRunAt)}`}</span>
          <span>{rs.intervalMin ? `${fmtInterval(rs.intervalMin)}, следующее ${relTime(rs.state.nextRunAt)}` : 'Обновление вручную'}</span>
        </div>
      )}
    </div>
  )
}
