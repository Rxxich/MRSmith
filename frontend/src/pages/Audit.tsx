import { type ReactNode, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { IconBox, IconCircleCheck, IconListDetails, IconSearch, IconStethoscope, IconWand } from '@tabler/icons-react'
import { api, errorText } from '../lib/api'
import { RULES, count, fmtBytes, fmtNum } from '../lib/format'
import { useAsync, useDebounced, useTitle } from '../lib/hooks'
import type { AuditCandidate, AuditFinding, AuditFixResult, AuditKind, AuditProvider, AuditReport, LookupHit, LookupMatch, LookupResult } from '../lib/types'
import { Button, Empty, Input, Loading, Notice, RuleText, Segmented, Select } from '../ui/core'
import { useOverlay } from '../ui/overlay'

const KIND_LABEL: Record<AuditKind, string> = {
  same: 'одинаковое',
  covered: 'покрыто шире',
  inner: 'лишнее внутри',
}

const PAGE = 50

type FixBody = {
  findings?: number[]
  keep?: Record<string, number>
  broad?: Record<string, boolean>
  pair?: { from: number; to: number; broad?: boolean }
  inner?: number[]
}

type Tab = 'lookup' | 'overlap'

export function Audit() {
  useTitle('Аудит')
  const [params, setParams] = useSearchParams()
  const tab: Tab = params.get('tab') === 'overlap' ? 'overlap' : 'lookup'
  const setTab = (t: Tab) => setParams(t === 'lookup' ? {} : { tab: t }, { replace: true })
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Аудит</h1>
          <p>Ищите, где встречается домен или IP, и находите дубли между наборами и файлами правил.</p>
        </div>
      </div>
      <div className="tabs" role="tablist">
        <button type="button" role="tab" aria-selected={tab === 'lookup'} onClick={() => setTab('lookup')}>
          <IconSearch size={16} aria-hidden /> Где встречается
        </button>
        <button type="button" role="tab" aria-selected={tab === 'overlap'} onClick={() => setTab('overlap')}>
          <IconStethoscope size={16} aria-hidden /> Пересечения и дубли
        </button>
      </div>
      {tab === 'lookup' ? <Lookup /> : <Overlap />}
    </div>
  )
}

// ---- lookup ----------------------------------------------------------------

const REL_LABEL: Record<LookupMatch['relation'], string> = {
  match: 'срабатывает',
  inside: 'вложенное',
  text: 'совпадает текст',
}

function Lookup() {
  const [params, setParams] = useSearchParams()
  const [q, setQ] = useState(params.get('q') || '')
  const [res, setRes] = useState<LookupResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const search = async (query: string) => {
    if (!query.trim()) return
    setBusy(true)
    setError('')
    try {
      setRes(await api.get<LookupResult>(`audit/lookup?q=${encodeURIComponent(query.trim())}`))
      setParams({ q: query.trim() }, { replace: true })
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    if (params.get('q')) void search(params.get('q')!)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const own = res?.hits.filter((h) => !h.system) || []
  const sources = res?.hits.filter((h) => h.system) || []
  const firing = own.filter((h) => h.matches.some((m) => m.relation === 'match')).length

  return (
    <div className="stack">
      <form
        className="lookup-form"
        onSubmit={(e) => {
          e.preventDefault()
          void search(q)
        }}
      >
        <Input mono value={q} onChange={(e) => setQ(e.target.value)} placeholder="admin.twitch.tv, 149.154.167.50 или просто twitch" aria-label="Что найти" autoFocus />
        <Button type="submit" variant="primary" busy={busy} disabled={!q.trim()} icon={<IconSearch size={17} />}>
          Найти
        </Button>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {!res ? (
        <p className="muted">
          Введите домен — панель покажет все наборы и файлы, где на него срабатывает правило (само правило, +.родитель, DOMAIN-KEYWORD) и где
          есть вложенные правила. Для IP — сети, в которые он входит. Одно слово ищется по тексту правил.
        </p>
      ) : own.length === 0 && sources.length === 0 ? (
        <section className="panel">
          <Empty icon={<IconCircleCheck size={30} />} title="Нигде не встречается">
            Проверено мест: {res.checked}. Ни одно правило не {res.mode === 'text' ? 'содержит' : 'срабатывает на'} «{res.query}».
          </Empty>
        </section>
      ) : (
        <>
          <p className="dim">
            {res.mode === 'text'
              ? `«${res.query}» встречается в тексте правил: ${count(own.length, ['место', 'места', 'мест'])} из ${res.checked} проверенных.`
              : firing
                ? `На «${res.query}» срабатывают правила в ${count(firing, ['месте', 'местах', 'местах'])} из ${res.checked} проверенных.`
                : `Правил, которые срабатывают на «${res.query}», нет — есть только вложенные.`}
            {firing > 1 && ' mihomo применит то, что стоит выше в списке rules, — остальные копии лишние.'}
          </p>
          <div className="hits">
            {own.map((h) => (
              <Hit key={h.id} hit={h} />
            ))}
          </div>
          {sources.length > 0 && (
            <details className="hits-sources">
              <summary className="small muted">Ещё в скачанных и загруженных источниках наборов: {sources.length}</summary>
              <div className="hits">
                {sources.map((h) => (
                  <Hit key={h.id} hit={h} />
                ))}
              </div>
            </details>
          )}
        </>
      )}
    </div>
  )
}

function Hit({ hit }: { hit: LookupHit }) {
  const bucket = hit.detail.split('/')[0]
  const folder = hit.detail.slice(bucket.length + 1).split('/').slice(0, -1).join('/')
  return (
    <section className="panel hit">
      <div className="hit-head">
        {hit.kind === 'ruleset' ? <IconListDetails size={16} className="muted" aria-hidden /> : <IconBox size={16} className="muted" aria-hidden />}
        {hit.kind === 'ruleset' ? (
          <Link to={`/rulesets/${hit.id.slice(3)}`}>{hit.name}</Link>
        ) : (
          <Link to={`/storage/${bucket}${folder ? `?p=${encodeURIComponent(folder + '/')}` : ''}`} className="mono">
            {hit.detail}
          </Link>
        )}
        <span className="tag tag-muted">{hit.kind === 'ruleset' ? 'набор' : hit.system ? 'источник набора' : 'файл'}</span>
      </div>
      <div className="hit-matches">
        {hit.matches.map((m) => (
          <div key={m.rule} className="hit-match">
            <span className="mono truncate">
              <RuleText rule={m.rule} />
            </span>
            <span className={m.relation === 'match' ? 'tag tag-domain' : 'tag tag-muted'}>{REL_LABEL[m.relation]}</span>
          </div>
        ))}
        {hit.more > 0 && <span className="small muted">и ещё {fmtNum(hit.more)}</span>}
      </div>
    </section>
  )
}

// ---- overlap ---------------------------------------------------------------

function Overlap() {
  const { toast, confirm } = useOverlay()
  const candidates = useAsync(() => api.get<AuditCandidate[]>('audit/candidates'), [])
  const [picked, setPicked] = useState<Set<string> | null>(null)
  const [showPicker, setShowPicker] = useState(false)
  const [report, setReport] = useState<AuditReport | null>(null)
  const [running, setRunning] = useState(false)
  const [fixing, setFixing] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (candidates.data && picked === null) setPicked(new Set(candidates.data.filter((c) => !c.system).map((c) => c.id)))
  }, [candidates.data, picked])

  const run = async () => {
    if (!picked) return
    setRunning(true)
    setError('')
    try {
      setReport(await api.post<AuditReport>('audit', { providers: [...picked] }))
      setShowPicker(false)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setRunning(false)
    }
  }

  const fix = async (body: FixBody, what: string) => {
    if (!report) return
    const ok = await confirm({
      title: 'Применить исправление?',
      body: (
        <div className="stack-s">
          <span>{what}</span>
          <span>
            В наборах убираются ровно эти строки — как исключения «только эта строка»; их видно во вкладке «Ручные правки», там же можно
            вернуть. Файлы в хранилище пересоберутся в том же формате, но комментарии и порядок строк в них не сохранятся.
          </span>
        </div>
      ),
      confirm: 'Исправить',
    })
    if (!ok) return
    setFixing(true)
    try {
      const res = await api.post<AuditFixResult[]>(`audit/${report.id}/fix`, body)
      const failed = res.filter((r) => r.error)
      const removed = res.reduce((n, r) => n + r.removed, 0)
      if (failed.length) toast(`Не получилось для: ${failed.map((r) => `${r.provider} (${r.error})`).join(', ')}`, 'error')
      toast(removed ? `Убрано: ${count(removed, RULES)}. Проверяю заново…` : 'Готово. Проверяю заново…')
      await candidates.reload()
      setReport(await api.post<AuditReport>('audit', { providers: [...(picked || [])] }))
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setFixing(false)
    }
  }

  if (candidates.error) return <Notice tone="error">{candidates.error.message}</Notice>
  if (!candidates.data || !picked) return <Loading />
  if (candidates.data.length === 0) {
    return (
      <section className="panel">
        <Empty icon={<IconStethoscope size={30} />} title="Проверять пока нечего">
          Аудит смотрит наборы правил и файлы .mrs, .yaml, .txt, .list в хранилище. Создайте набор или загрузите файлы.
        </Empty>
      </section>
    )
  }
  const sets = candidates.data.filter((c) => c.kind === 'ruleset' && picked.has(c.id)).length
  const files = candidates.data.filter((c) => c.kind === 'object' && picked.has(c.id)).length

  return (
    <div className="stack">
      <section className="panel">
        <div className="run-bar">
          <div className="grow">
            <p className="dim">
              Ищет правила, которые повторяются в разных местах или уже покрыты более широким правилом. mihomo срабатывает на первом
              совпадении, поэтому такие копии ничего не делают — только раздувают файлы.
            </p>
            <p className="small muted mt-s">
              Проверяются {count(sets, ['набор', 'набора', 'наборов'])} и {count(files, ['файл', 'файла', 'файлов'])}.{' '}
              <button type="button" className="link-btn" onClick={() => setShowPicker((v) => !v)}>
                {showPicker ? 'Свернуть' : 'Изменить'}
              </button>
            </p>
          </div>
          <Button variant="primary" busy={running} disabled={!picked.size || fixing} onClick={run} icon={<IconStethoscope size={17} />}>
            {report ? 'Проверить заново' : 'Найти дубли'}
          </Button>
        </div>
        {showPicker && <Picker candidates={candidates.data} picked={picked} onChange={setPicked} />}
      </section>
      {error && <Notice tone="error">{error}</Notice>}
      {report && <Results report={report} fixing={fixing} onFix={fix} />}
    </div>
  )
}

function Picker({ candidates, picked, onChange }: { candidates: AuditCandidate[]; picked: Set<string>; onChange: (s: Set<string>) => void }) {
  const sets = candidates.filter((c) => c.kind === 'ruleset')
  const files = candidates.filter((c) => c.kind === 'object')
  const toggle = (id: string) => {
    const n = new Set(picked)
    if (n.has(id)) n.delete(id)
    else n.add(id)
    onChange(n)
  }
  const group = (title: string, icon: ReactNode, items: AuditCandidate[], empty: string) => (
    <div className="pick-group">
      <div className="spread">
        <span className="stage-title">
          {icon} {title}
        </span>
        {items.length > 1 && (
          <span className="row-flex">
            <button type="button" className="link-btn" onClick={() => onChange(new Set([...picked, ...items.map((c) => c.id)]))}>
              все
            </button>
            <button type="button" className="link-btn" onClick={() => onChange(new Set([...picked].filter((id) => !items.some((c) => c.id === id))))}>
              ни одного
            </button>
          </span>
        )}
      </div>
      {items.length === 0 ? (
        <p className="muted small">{empty}</p>
      ) : (
        <div className="pick-list">
          {items.map((c) => (
            <label key={c.id} className="pick-item">
              <input type="checkbox" checked={picked.has(c.id)} onChange={() => toggle(c.id)} />
              <span className="truncate grow" title={c.detail}>
                {c.kind === 'ruleset' ? c.name : c.detail}
                {c.system && <span className="muted"> (источник набора)</span>}
              </span>
              <span className="muted small nowrap">{c.kind === 'ruleset' ? fmtNum(c.rules) : fmtBytes(c.size)}</span>
            </label>
          ))}
        </div>
      )}
    </div>
  )
  return (
    <div className="panel-body pick-grid picker-body">
      {group('Наборы правил', <IconListDetails size={15} />, sets, 'Наборов нет.')}
      {group('Файлы в хранилище', <IconBox size={15} />, files, 'Файлов правил в хранилище нет.')}
    </div>
  )
}

function providerName(p: AuditProvider | undefined) {
  if (!p) return '?'
  return p.kind === 'ruleset' ? p.name : p.detail
}

function Results({ report, fixing, onFix }: { report: AuditReport; fixing: boolean; onFix: (b: FixBody, what: string) => void }) {
  const t = report.totals
  const P = report.providers
  const failed = P.filter((p) => p.error)
  return (
    <>
      <div className="ledger">
        <div>
          <b>{fmtNum(t.rules)}</b>
          <span>правил в {count(t.providers, ['источнике', 'источниках', 'источниках'])}</span>
        </div>
        <div>
          <b>{fmtNum(t.same)}</b>
          <span>одинаковых в нескольких местах</span>
        </div>
        <div>
          <b>{fmtNum(t.covered)}</b>
          <span>покрыты правилом шире</span>
        </div>
        <div>
          <b>{fmtNum(t.inner)}</b>
          <span>лишних внутри файла</span>
        </div>
        <div>
          <b>{fmtNum(t.removable)}</b>
          <span>можно убрать</span>
        </div>
      </div>

      {failed.length > 0 && (
        <Notice tone="warn">
          Не удалось прочитать: {failed.map((p) => `${providerName(p)} (${p.error})`).join(', ')}.
        </Notice>
      )}
      {report.truncated && (
        <Notice>Находок очень много — показаны первые 50 000. Исправьте крупные пересечения и запустите аудит снова.</Notice>
      )}

      {t.findings === 0 ? (
        <section className="panel">
          <Empty icon={<IconCircleCheck size={30} color="var(--patina)" />} title="Пересечений нет">
            Каждое правило встречается один раз и не перекрыто другими — дублей нет.
          </Empty>
        </section>
      ) : (
        <>
          <section className="panel">
            <div className="panel-head">
              <div>
                <h2>Где пересекаются</h2>
                <p>Сколько правил можно убрать из одного места, потому что они уже есть в другом</p>
              </div>
            </div>
            <div className="pairs">
              {report.pairs.map((pr) => {
                const from = P[pr.from]
                const to = P[pr.to]
                if (pr.from === pr.to) {
                  return (
                    <div key={`${pr.from}-${pr.to}`} className="pair">
                      <div className="grow">
                        <b>{providerName(from)}</b>
                        <div className="small muted">
                          {count(pr.inner, RULES)} покрывают более широкие правила из {from.kind === 'ruleset' ? 'этого же набора' : 'этого же файла'}
                        </div>
                      </div>
                      <Button
                        size="sm"
                        disabled={fixing}
                        icon={<IconWand size={15} />}
                        onClick={() =>
                          onFix(
                            { inner: [pr.from] },
                            from.kind === 'ruleset'
                              ? `В наборе «${providerName(from)}» включится оптимизация — лишние правила уйдут при сборке.`
                              : `Из файла ${providerName(from)} уберутся правила, которые уже покрыты внутри него.`,
                          )
                        }
                      >
                        {from.kind === 'ruleset' ? 'Включить оптимизацию' : 'Почистить файл'}
                      </Button>
                    </div>
                  )
                }
                return (
                  <div key={`${pr.from}-${pr.to}`} className="pair">
                    <div className="grow">
                      <span>
                        Из <b>{providerName(from)}</b> можно убрать {count(pr.same + pr.covered, RULES)} — они уже есть в <b>{providerName(to)}</b>
                      </span>
                      <div className="small muted">
                        {[pr.same && `одинаковых ${fmtNum(pr.same)}`, pr.covered && `покрыто правилом шире ${fmtNum(pr.covered)}`].filter(Boolean).join(', ')}
                      </div>
                    </div>
                    <div className="pair-actions">
                      <Button
                        size="sm"
                        disabled={fixing}
                        icon={<IconWand size={15} />}
                        onClick={() =>
                          onFix(
                            { pair: { from: pr.from, to: pr.to } },
                            `Из «${providerName(from)}» уберутся ${count(pr.same + pr.covered, RULES)}, которые уже есть в «${providerName(to)}».`,
                          )
                        }
                      >
                        Убрать из {providerName(from)}
                      </Button>
                      {pr.wide > 0 && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={fixing}
                          onClick={() =>
                            onFix(
                              { pair: { from: pr.from, to: pr.to, broad: true } },
                              `Из «${providerName(to)}» уберутся широкие правила (${fmtNum(pr.wide)}), которые перекрывают правила «${providerName(from)}». Сами правила «${providerName(from)}» останутся.`,
                            )
                          }
                        >
                          Или убрать широкие из {providerName(to)}: {fmtNum(pr.wide)}
                        </Button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </section>
          <Findings report={report} fixing={fixing} onFix={onFix} />
        </>
      )}
    </>
  )
}

function Findings({ report, fixing, onFix }: { report: AuditReport; fixing: boolean; onFix: (b: FixBody, what: string) => void }) {
  const P = report.providers
  const [kind, setKind] = useState<'' | AuditKind>('')
  const [provider, setProvider] = useState(-1)
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [keep, setKeep] = useState<Record<number, number>>({})
  const [broad, setBroad] = useState<Record<number, boolean>>({})
  useEffect(() => setOffset(0), [kind, provider, dq])
  useEffect(() => {
    setSelected(new Set())
    setKeep({})
    setBroad({})
  }, [report.id])
  const data = useAsync(
    () =>
      api.get<{ total: number; items: AuditFinding[] }>(
        `audit/${report.id}/findings?kind=${kind}&provider=${provider}&q=${encodeURIComponent(dq)}&offset=${offset}&limit=${PAGE}`,
      ),
    [report.id, kind, provider, dq, offset],
  )
  const t = report.totals

  const toggle = (id: number) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Находки</h2>
        {selected.size > 0 && (
          <Button
            size="sm"
            variant="primary"
            disabled={fixing}
            icon={<IconWand size={15} />}
            onClick={() => onFix({ findings: [...selected], keep, broad }, `Будут исправлены выбранные находки: ${selected.size}.`)}
          >
            Исправить выбранные: {selected.size}
          </Button>
        )}
      </div>
      <div className="panel-body stack">
        <div className="spread">
          <Segmented
            label="Тип находки"
            value={kind}
            onChange={setKind}
            options={[
              { value: '', label: `Все ${fmtNum(t.findings)}` },
              { value: 'same', label: `Одинаковые ${fmtNum(t.same)}` },
              { value: 'covered', label: `Покрыто шире ${fmtNum(t.covered)}` },
              { value: 'inner', label: `Внутри ${fmtNum(t.inner)}` },
            ]}
          />
          <div className="row-flex">
            <Select value={provider} onChange={(e) => setProvider(Number(e.target.value))} aria-label="Источник" className="select narrow">
              <option value={-1}>Все наборы и файлы</option>
              {P.map((p, i) => (
                <option key={p.id} value={i}>
                  {providerName(p)}
                </option>
              ))}
            </Select>
            <div className="search-box">
              <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Домен или IP, например twitch" aria-label="Поиск" />
              <IconSearch size={16} className="muted search-icon" aria-hidden />
            </div>
          </div>
        </div>

        {!data.data ? (
          <Loading />
        ) : data.data.total === 0 ? (
          <p className="muted">Ничего не найдено{dq ? ` по «${dq}»` : ''}.</p>
        ) : (
          <>
            <div className="findings">
              {data.data.items.map((f) => (
                <FindingRow
                  key={f.id}
                  f={f}
                  providers={P}
                  checked={selected.has(f.id)}
                  onCheck={() => toggle(f.id)}
                  keep={keep[f.id]}
                  onKeep={(p) => setKeep((k) => ({ ...k, [f.id]: p }))}
                  broad={!!broad[f.id]}
                  onBroad={(v) => setBroad((b) => ({ ...b, [f.id]: v }))}
                  fixing={fixing}
                  onFix={() =>
                    onFix(
                      {
                        findings: [f.id],
                        keep: keep[f.id] !== undefined ? { [f.id]: keep[f.id] } : undefined,
                        broad: broad[f.id] ? { [f.id]: true } : undefined,
                      },
                      broad[f.id]
                        ? `Уберётся более широкое правило, которое покрывает ${f.value}; само ${f.value} останется.`
                        : `Правило ${f.value} будет убрано там, где оно лишнее.`,
                    )
                  }
                />
              ))}
            </div>
            <div className="pager">
              <span>
                {fmtNum(offset + 1)}–{fmtNum(Math.min(offset + PAGE, data.data.total))} из {fmtNum(data.data.total)}
              </span>
              <div className="row-flex">
                <Button size="sm" onClick={() => setSelected(new Set([...selected, ...data.data!.items.map((f) => f.id)]))}>
                  Выбрать на странице
                </Button>
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
      </div>
    </section>
  )
}

interface RowProps {
  f: AuditFinding
  providers: AuditProvider[]
  checked: boolean
  onCheck: () => void
  keep: number | undefined
  onKeep: (p: number) => void
  broad: boolean
  onBroad: (v: boolean) => void
  fixing: boolean
  onFix: () => void
}

function FindingRow({ f, providers, checked, onCheck, keep, onKeep, broad, onBroad, fixing, onFix }: RowProps) {
  const keepable = f.kind === 'same' ? f.occ.filter((o) => !o.inner) : []
  const keeper = keep ?? f.occ.find((o) => o.keep)?.p
  const canBroad = f.kind !== 'same' && f.occ.some((o) => o.keep && !o.union)
  const shown = f.occ.slice(0, 8)
  return (
    <div className={checked ? 'finding is-checked' : 'finding'}>
      <input type="checkbox" checked={checked} onChange={onCheck} aria-label={`Выбрать ${f.value}`} />
      <div className="grow stack-s">
        <div className="row-flex">
          <span className="finding-value">
            <RuleText rule={f.value} />
          </span>
          <span className={`tag ${f.kind === 'same' ? 'tag-muted' : f.kind === 'covered' ? 'tag-classical' : 'tag-warn'}`}>{KIND_LABEL[f.kind]}</span>
        </div>
        <div className="occ-list">
          {shown.map((o, i) => {
            let remove = f.kind === 'same' && keep !== undefined ? o.p !== keep && !o.inner : !!o.remove
            let stays = f.kind === 'same' ? o.p === keeper : !!o.keep
            if (f.kind !== 'same' && broad) {
              remove = !!o.keep && !o.union
              stays = !!o.remove
            }
            return (
              <div key={i} className="occ">
                <span className="truncate occ-where">{providerName(providers[o.p])}</span>
                <span className="occ-rule mono truncate">{o.rule !== f.value || f.kind !== 'same' ? <RuleText rule={o.rule} /> : null}</span>
                {remove ? (
                  <span className="tag tag-error">убрать</span>
                ) : o.inner ? (
                  <span className="tag tag-warn">лишнее внутри</span>
                ) : stays ? (
                  <span className="tag tag-ok">останется</span>
                ) : null}
              </div>
            )
          })}
          {f.occ.length > shown.length && <span className="small muted">и ещё в {f.occ.length - shown.length}</span>}
        </div>
      </div>
      <div className="finding-actions">
        {canBroad && (
          <Segmented
            label="Что убрать"
            value={broad ? 'broad' : 'narrow'}
            onChange={(v) => onBroad(v === 'broad')}
            options={[
              { value: 'narrow', label: 'Убрать узкое' },
              { value: 'broad', label: 'Убрать широкое' },
            ]}
          />
        )}
        {keepable.length > 1 && (
          <Select value={keeper} onChange={(e) => onKeep(Number(e.target.value))} aria-label="Где оставить" className="select keep-select">
            {keepable.map((o) => (
              <option key={o.p} value={o.p}>
                Оставить в {providerName(providers[o.p])}
              </option>
            ))}
          </Select>
        )}
        <Button size="sm" disabled={fixing} onClick={onFix} icon={<IconWand size={15} />}>
          Исправить
        </Button>
      </div>
    </div>
  )
}
