import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { IconBox, IconClipboardText, IconDeviceFloppy, IconDownload, IconFile, IconLink, IconListDetails, IconScan } from '@tabler/icons-react'
import { useMe } from '../app/auth'
import { api, downloadPost, errorText, saveBlob } from '../lib/api'
import { BEHAVIOR_LABEL, FORMAT_INFO, INPUT_LABEL, RULES, count, droppedText, fmtBytes, fmtNum } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { Behavior, Bucket, ConvertSummary, InputFormat, OutputFormat } from '../lib/types'
import { BehaviorTag, Button, Field, Input, Notice, RuleText, Segmented, Select, Switch, Textarea } from '../ui/core'
import { Dropzone } from '../ui/files'
import { useOverlay } from '../ui/overlay'

type Source = 'file' | 'url' | 'text' | 'object'

export function Converter() {
  useTitle('Конвертер')
  const me = useMe()
  const { toast } = useOverlay()
  const [params] = useSearchParams()
  const [source, setSource] = useState<Source>(params.get('bucket') ? 'object' : 'file')
  const [file, setFile] = useState<File | null>(null)
  const [url, setUrl] = useState('')
  const [text, setText] = useState('')
  const [obj] = useState({ bucket: params.get('bucket') || '', key: params.get('key') || '' })
  const [inputFormat, setInputFormat] = useState<InputFormat>('auto')
  const [behavior, setBehavior] = useState<Behavior>('auto')
  const [bareAsSuffix, setBareAsSuffix] = useState(false)
  const [optimize, setOptimize] = useState(false)
  const [aggregateIP, setAggregateIP] = useState(false)
  const [noResolve, setNoResolve] = useState(false)
  const [summary, setSummary] = useState<ConvertSummary | null>(null)
  const [target, setTarget] = useState<OutputFormat>('mrs')
  const [busy, setBusy] = useState<'' | 'inspect' | 'download' | 'save'>('')
  const [error, setError] = useState('')
  const [saveOpen, setSaveOpen] = useState(false)
  const buckets = useAsync(() => api.get<Bucket[]>('buckets'), [])
  const [saveBucket, setSaveBucket] = useState('')
  const [saveKey, setSaveKey] = useState('')

  const ready = source === 'file' ? !!file : source === 'url' ? !!url.trim() : source === 'text' ? !!text.trim() : !!obj.key

  const form = (mode: string) => {
    const fd = new FormData()
    fd.append('mode', mode)
    fd.append('source', source)
    fd.append('inputFormat', inputFormat)
    fd.append('behavior', behavior)
    fd.append('bareAsSuffix', String(bareAsSuffix))
    fd.append('optimize', String(optimize))
    fd.append('aggregateIP', String(aggregateIP))
    fd.append('noResolve', String(noResolve))
    fd.append('target', target)
    if (source === 'url') fd.append('url', url.trim())
    if (source === 'object') {
      fd.append('bucket', obj.bucket)
      fd.append('key', obj.key)
    }
    if (mode === 'save') {
      fd.append('saveBucket', saveBucket)
      fd.append('saveKey', saveKey)
    }
    if (source === 'text') fd.append('text', text)
    if (source === 'file' && file) fd.append('file', file, file.name)
    return fd
  }

  const inspect = async () => {
    setBusy('inspect')
    setError('')
    try {
      const s = await api.post<ConvertSummary>('convert', form('inspect'))
      setSummary(s)
      if (!s.result.supported[target]) {
        const first = (['mrs', 'yaml', 'txt', 'list'] as OutputFormat[]).find((f) => s.result.supported[f])
        if (first) setTarget(first)
      }
    } catch (e) {
      setSummary(null)
      setError(errorText(e))
    } finally {
      setBusy('')
    }
  }

  // Re-run when options change after a first look, so numbers stay honest.
  useEffect(() => {
    if (summary && ready) void inspect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inputFormat, behavior, bareAsSuffix, optimize, aggregateIP])

  useEffect(() => {
    if (source === 'object' && obj.key) void inspect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const download = async () => {
    setBusy('download')
    try {
      const { blob, name } = await downloadPost('convert', form('download'))
      saveBlob(blob, name)
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy('')
    }
  }

  const save = async () => {
    setBusy('save')
    try {
      const s = await api.post<ConvertSummary>('convert', form('save'))
      toast(
        <>
          Сохранено: <Link to={`/storage/${s.result.saved?.bucket}`}>{s.result.saved?.bucket}/{s.result.saved?.key}</Link>
        </>,
      )
      setSaveOpen(false)
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy('')
    }
  }

  const baseName = (summary?.input.name || 'rules').replace(/\.[^.]+$/, '')


  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Конвертер</h1>
          <p>Разберите файл правил, посмотрите, что внутри, и сохраните его в другом формате. Для постоянно обновляемых списков создайте набор правил.</p>
        </div>
      </div>

      <div className="convert-layout">
        <section className="panel">
          <div className="panel-head">
            <h2>Исходный файл</h2>
            <Segmented
              label="Откуда взять файл"
              value={source}
              onChange={(v) => {
                setSource(v)
                setSummary(null)
              }}
              options={[
                { value: 'file', label: 'Файл', icon: <IconFile size={15} /> },
                { value: 'url', label: 'Ссылка', icon: <IconLink size={15} /> },
                { value: 'text', label: 'Текст', icon: <IconClipboardText size={15} /> },
                ...(obj.key ? [{ value: 'object' as Source, label: 'Хранилище', icon: <IconBox size={15} /> }] : []),
              ]}
            />
          </div>
          <div className="panel-body stack">
            {source === 'file' && (
              <Dropzone
                multiple={false}
                onFiles={([f]) => {
                  setFile(f.file)
                  setSummary(null)
                }}
                title={file ? file.name : 'Перетащите файл или нажмите, чтобы выбрать'}
                hint={file ? fmtBytes(file.size) : `MRS, YAML, TXT, LIST, hosts, AdBlock, dnsmasq — до ${fmtBytes(me.limits.maxFetch)}`}
              />
            )}
            {source === 'url' && (
              <Field label="Ссылка на файл">
                {(id) => <Input id={id} mono type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/rules.mrs" />}
              </Field>
            )}
            {source === 'text' && (
              <Field label="Правила" hint="По одному в строке, в любом из поддерживаемых синтаксисов">
                {(id) => <Textarea id={id} rows={9} value={text} onChange={(e) => setText(e.target.value)} placeholder={'+.google.com\nDOMAIN-SUFFIX,youtube.com\n1.1.1.0/24'} />}
              </Field>
            )}
            {source === 'object' && (
              <div className="src-line">
                <IconBox size={16} className="muted" aria-hidden />
                <span className="mono">
                  {obj.bucket}/{obj.key}
                </span>
              </div>
            )}

            <div className="form-grid">
              <Field label="Формат исходника">
                {(id) => (
                  <Select id={id} value={inputFormat} onChange={(e) => setInputFormat(e.target.value as InputFormat)}>
                    {(Object.keys(INPUT_LABEL) as InputFormat[]).map((f) => (
                      <option key={f} value={f}>
                        {INPUT_LABEL[f]}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="Behavior результата">
                {(id) => (
                  <Select id={id} value={behavior} onChange={(e) => setBehavior(e.target.value as Behavior)}>
                    {(Object.keys(BEHAVIOR_LABEL) as Behavior[]).map((b) => (
                      <option key={b} value={b}>
                        {BEHAVIOR_LABEL[b]}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
            </div>
            <Switch checked={bareAsSuffix} onChange={setBareAsSuffix} label="Домены без префикса — вместе с поддоменами" hint="example.com станет +.example.com" />
            <Switch checked={optimize} onChange={setOptimize} label="Убирать перекрытые домены" />
            <Switch checked={aggregateIP} onChange={setAggregateIP} label="Склеивать соседние IP-сети" />
            <Switch checked={noResolve} onChange={setNoResolve} label="no-resolve для IP в формате LIST" />
            <div>
              <Button variant="primary" busy={busy === 'inspect'} disabled={!ready} onClick={inspect} icon={<IconScan size={17} />}>
                Разобрать
              </Button>
            </div>
          </div>
        </section>

        <section className="panel">
          <div className="panel-head">
            <h2>Результат</h2>
            {summary && <BehaviorTag behavior={summary.result.behavior} />}
          </div>
          <div className="panel-body stack">
            {error && <Notice tone="error">{error}</Notice>}
            {!summary && !error && <p className="muted">Выберите файл и нажмите «Разобрать» — здесь появится, что внутри, и кнопки для сохранения.</p>}
            {summary && (
              <>
                <dl className="facts">
                  <dt>Определён формат</dt>
                  <dd>{summary.input.format}</dd>
                  <dt>Строк с правилами</dt>
                  <dd>{fmtNum(summary.input.lines)}</dd>
                  {summary.input.invalidCount > 0 && (
                    <>
                      <dt>Не распознано</dt>
                      <dd className="text-warn">{fmtNum(summary.input.invalidCount)}</dd>
                    </>
                  )}
                  {summary.result.optimized > 0 && (
                    <>
                      <dt>Убрано перекрытых</dt>
                      <dd>{fmtNum(summary.result.optimized)}</dd>
                    </>
                  )}
                  {summary.result.skipped > 0 && (
                    <>
                      <dt>Не подошло к {summary.result.behavior}</dt>
                      <dd className="text-warn" title={droppedText(summary.result.dropped)}>
                        {fmtNum(summary.result.skipped)}
                      </dd>
                    </>
                  )}
                  <dt>В результате</dt>
                  <dd>
                    <b>{count(summary.result.count, RULES)}</b>
                  </dd>
                </dl>
                {summary.input.invalidCount > 0 && (
                  <details>
                    <summary className="small text-warn">Какие строки не распознаны</summary>
                    <pre className="code mt-s">{summary.input.invalid.join('\n')}</pre>
                  </details>
                )}
                {summary.result.sample.length > 0 && (
                  <div className="entries flat-preview">
                    {summary.result.sample.slice(0, 120).map((r) => (
                      <div key={r} className="entry">
                        <span className="entry-text">
                          <RuleText rule={r} />
                        </span>
                      </div>
                    ))}
                    {summary.result.count > 120 && <div className="entry muted">и ещё {fmtNum(summary.result.count - 120)}</div>}
                  </div>
                )}

                <div className="stack-s">
                  <span className="field-label">Сохранить как</span>
                  <div className="formats-row">
                    {(Object.keys(FORMAT_INFO) as OutputFormat[]).map((f) => (
                      <button
                        key={f}
                        type="button"
                        className="format-btn"
                        aria-pressed={target === f}
                        disabled={!summary.result.supported[f]}
                        onClick={() => setTarget(f)}
                        title={summary.result.supported[f] ? FORMAT_INFO[f].note : 'Недоступно для этого набора'}
                      >
                        <b>{FORMAT_INFO[f].title}</b>
                        <small>{f === 'list' ? 'classical' : f === 'mrs' ? 'бинарный' : 'текст'}</small>
                      </button>
                    ))}
                  </div>
                  {target === 'mrs' && summary.result.mrs && (
                    <MRSPartNotice part={summary.result.mrs} onBehavior={setBehavior} />
                  )}
                  {summary.result.mrsError && <span className="small muted">MRS: {summary.result.mrsError}.</span>}
                </div>

                <div className="row-flex">
                  <Button variant="primary" busy={busy === 'download'} onClick={download} disabled={!summary.result.supported[target]} icon={<IconDownload size={17} />}>
                    Скачать {baseName}.{target}
                  </Button>
                  <Button
                    onClick={() => {
                      setSaveKey(`${baseName}.${target}`)
                      setSaveBucket((b) => b || buckets.data?.find((x) => !x.system)?.name || buckets.data?.[0]?.name || '')
                      setSaveOpen((v) => !v)
                    }}
                    disabled={!summary.result.supported[target]}
                    icon={<IconDeviceFloppy size={17} />}
                  >
                    В хранилище
                  </Button>
                  {(source === 'url' || source === 'object') && (
                    <Link
                      className="btn btn-ghost"
                      to={source === 'url' ? `/rulesets/new?url=${encodeURIComponent(url.trim())}` : `/rulesets/new?bucket=${encodeURIComponent(obj.bucket)}&key=${encodeURIComponent(obj.key)}`}
                    >
                      <IconListDetails size={17} /> Создать набор
                    </Link>
                  )}
                </div>

                {saveOpen && (
                  <div className="source-edit">
                    {buckets.data?.length ? (
                      <>
                        <div className="form-grid">
                          <Field label="Бакет">
                            {(id) => (
                              <Select id={id} value={saveBucket} onChange={(e) => setSaveBucket(e.target.value)}>
                                {buckets.data?.map((b) => (
                                  <option key={b.name} value={b.name}>
                                    {b.name}
                                    {b.public ? ' (публичный)' : ''}
                                  </option>
                                ))}
                              </Select>
                            )}
                          </Field>
                          <Field label="Имя файла" hint="Можно с папкой: rules/ads.mrs">
                            {(id) => <Input id={id} mono value={saveKey} onChange={(e) => setSaveKey(e.target.value)} />}
                          </Field>
                        </div>
                        <div>
                          <Button variant="primary" busy={busy === 'save'} onClick={save} disabled={!saveBucket || !saveKey.trim()}>
                            Сохранить
                          </Button>
                        </div>
                      </>
                    ) : (
                      <span className="muted">
                        Сначала создайте бакет в разделе <Link to="/storage">Хранилище</Link>.
                      </span>
                    )}
                  </div>
                )}
              </>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}

/** Explains what a mixed list keeps and loses in an MRS file. */
function MRSPartNotice({ part, onBehavior }: { part: NonNullable<ConvertSummary['result']['mrs']>; onBehavior: (b: Behavior) => void }) {
  const domains = part.behavior === 'domain'
  const other = part.dropped?.[domains ? 'ip' : 'domains'] || 0
  return (
    <Notice>
      <p>
        MRS хранит только домены или только IP-сети, поэтому в файл попадут{' '}
        <b>{domains ? count(part.count, ['домен', 'домена', 'доменов']) : count(part.count, ['IP-сеть', 'IP-сети', 'IP-сетей'])}</b>
        {part.translated > 0 && <> (из них {fmtNum(part.translated)} переведено из DOMAIN-REGEX)</>}.
        {part.dropped && <> Не войдут: {droppedText(part.dropped)}.</>}
      </p>
      <p className="small muted mt-xs">
        YAML, TXT и LIST сохраняют весь список.
        {other > 0 && (
          <>
            {' '}
            <button type="button" className="link-btn" onClick={() => onBehavior(domains ? 'ipcidr' : 'domain')}>
              {domains ? 'Сделать MRS из IP-сетей' : 'Сделать MRS из доменов'}
            </button>
          </>
        )}
      </p>
    </Notice>
  )
}
