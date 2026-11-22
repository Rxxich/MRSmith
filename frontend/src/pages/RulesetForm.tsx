import { type FormEvent, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { IconBox, IconDeviceFloppy, IconFile, IconLink, IconTrash } from '@tabler/icons-react'
import { useMe } from '../app/auth'
import { api, errorText, upload } from '../lib/api'
import { BEHAVIOR_LABEL, FORMAT_INFO, INPUT_LABEL, INTERVALS, fmtBytes, publicBase } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { Behavior, ExcludeMode, InputFormat, Integrations, OutputFormat, Ruleset, RulesetInput, SourceInput } from '../lib/types'
import { Button, Field, Input, Loading, Notice, Select, Switch, Textarea } from '../ui/core'
import { Dropzone } from '../ui/files'
import { useOverlay } from '../ui/overlay'
import { ObjectPicker } from './ObjectPicker'
import { MODES } from './ExcludeDialog'

const EMPTY: RulesetInput = {
  name: '',
  slug: '',
  description: '',
  behavior: 'auto',
  formats: ['mrs'],
  sources: [],
  include: [],
  exclude: [],
  optimize: false,
  aggregateIP: false,
  noResolve: false,
  intervalMin: 360,
  private: false,
  notify: false,
}

function toInput(rs: Ruleset): RulesetInput {
  return {
    name: rs.name,
    slug: rs.slug,
    description: rs.description,
    behavior: rs.behavior,
    formats: rs.formats,
    sources: rs.sources.map((s) => ({ id: s.id, type: s.type, url: s.url, bucket: s.bucket, key: s.key, format: s.format, bareAsSuffix: s.bareAsSuffix })),
    include: rs.include,
    exclude: rs.exclude,
    optimize: rs.optimize,
    aggregateIP: rs.aggregateIP,
    noResolve: rs.noResolve,
    intervalMin: rs.intervalMin,
    private: rs.private,
    notify: rs.notify,
  }
}

const lines = (s: string) =>
  s
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)

export function RulesetForm() {
  const { id } = useParams()
  const editing = !!id
  useTitle(editing ? 'Настройки набора' : 'Новый набор')
  const me = useMe()
  const nav = useNavigate()
  const [params] = useSearchParams()
  const { toast } = useOverlay()
  const [form, setForm] = useState<RulesetInput | null>(editing ? null : EMPTY)
  const [include, setInclude] = useState('')
  const [exclude, setExclude] = useState('')
  const [excludeMode, setExcludeMode] = useState<ExcludeMode>('exact')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState<string[]>([])
  const [picker, setPicker] = useState(false)
  const integrations = useAsync(() => api.get<Integrations>('integrations'), [])
  const telegram = integrations.data?.telegram.enabled

  useEffect(() => {
    if (editing) {
      api
        .get<Ruleset>(`rulesets/${id}`)
        .then((rs) => {
          const f = toInput(rs)
          setForm(f)
          setInclude(f.include.join('\n'))
        })
        .catch((e) => setError(errorText(e)))
      return
    }
    const url = params.get('url')
    const bucket = params.get('bucket')
    const key = params.get('key')
    if (url) setForm({ ...EMPTY, sources: [{ type: 'url', url, format: 'auto', bareAsSuffix: false }] })
    else if (bucket && key) setForm({ ...EMPTY, sources: [{ type: 'object', bucket, key, format: 'auto', bareAsSuffix: false }] })
  }, [editing, id, params])

  const set = <K extends keyof RulesetInput>(k: K, v: RulesetInput[K]) => setForm((f) => (f ? { ...f, [k]: v } : f))
  const setSource = (i: number, patch: Partial<SourceInput>) =>
    setForm((f) => (f ? { ...f, sources: f.sources.map((s, j) => (j === i ? { ...s, ...patch } : s)) } : f))

  const slugPreview = useMemo(() => form?.slug || translit(form?.name || '') || 'имя-набора', [form?.slug, form?.name])

  if (!form) return error ? <Notice tone="error">{error}</Notice> : <Loading />

  const mrsBlocked = form.behavior === 'classical'

  const addFiles = async (files: { file: File }[]) => {
    for (const { file } of files) {
      if (file.size > me.limits.maxFetch) {
        toast(`${file.name}: больше ${fmtBytes(me.limits.maxFetch)}`, 'error')
        continue
      }
      setUploading((u) => [...u, file.name])
      try {
        const fd = new FormData()
        fd.append('file', file, file.name)
        const res = await upload<{ bucket: string; key: string }>('rulesets/sources/upload', fd).promise
        setForm((f) => (f ? { ...f, sources: [...f.sources, { type: 'object', bucket: res.bucket, key: res.key, format: 'auto', bareAsSuffix: false }] } : f))
      } catch (e) {
        toast(`${file.name}: ${errorText(e)}`, 'error')
      } finally {
        setUploading((u) => u.filter((n) => n !== file.name))
      }
    }
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    const fresh = lines(exclude)
    const body: RulesetInput = {
      ...form,
      include: lines(include),
      exclude: [...form.exclude.filter((e) => !fresh.includes(e.rule)), ...fresh.map((rule) => ({ rule, mode: excludeMode }))],
    }
    if (mrsBlocked) body.formats = body.formats.filter((f) => f !== 'mrs')
    try {
      const rs = editing ? await api.put<Ruleset>(`rulesets/${id}`, body) : await api.post<Ruleset>('rulesets', body)
      toast(editing ? 'Изменения сохранены, набор пересобирается' : 'Набор создан, идёт первая сборка')
      nav(`/rulesets/${rs.id}`)
    } catch (err) {
      setError(errorText(err))
      window.scrollTo({ top: 0, behavior: 'smooth' })
    } finally {
      setSaving(false)
    }
  }

  return (
    <form className="stack" onSubmit={submit}>
      <div className="page-head">
        <div>
          <div className="crumbs">
            <Link to="/rulesets">Наборы правил</Link>
            {editing && (
              <>
                <span aria-hidden>/</span>
                <Link to={`/rulesets/${id}`}>{form.name}</Link>
              </>
            )}
          </div>
          <h1>{editing ? 'Настройки набора' : 'Новый набор правил'}</h1>
        </div>
        <div className="row-flex">
          <Link to={editing ? `/rulesets/${id}` : '/rulesets'} className="btn btn-ghost">
            Отмена
          </Link>
          <Button type="submit" variant="primary" busy={saving} icon={<IconDeviceFloppy size={17} />}>
            {editing ? 'Сохранить' : 'Создать набор'}
          </Button>
        </div>
      </div>

      {error && <Notice tone="error">{error}</Notice>}

      <section className="panel">
        <div className="panel-head">
          <h2>Название и ссылка</h2>
        </div>
        <div className="panel-body form-grid">
          <Field label="Название">
            {(fid) => <Input id={fid} value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="Например, Реклама и трекеры" required maxLength={80} />}
          </Field>
          <Field label="Имя файла в ссылке" hint={<span className="mono">{publicBase(me)}/r/{slugPreview}.mrs</span>}>
            {(fid) => (
              <Input
                id={fid}
                mono
                value={form.slug}
                onChange={(e) => set('slug', e.target.value.toLowerCase())}
                placeholder={translit(form.name) || 'ads-and-trackers'}
                pattern="[a-z0-9][a-z0-9._\-]{0,62}"
                title="Латиница, цифры, точка, дефис и подчёркивание"
              />
            )}
          </Field>
          <Field label="Описание" className="span-2" hint="Видно только в панели">
            {(fid) => <Input id={fid} value={form.description} onChange={(e) => set('description', e.target.value)} maxLength={500} />}
          </Field>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>Источники</h2>
            <p>Ссылки и файлы объединяются в один набор, дубли убираются</p>
          </div>
          <div className="row-flex">
            <Button size="sm" icon={<IconLink size={15} />} onClick={() => set('sources', [...form.sources, { type: 'url', url: '', format: 'auto', bareAsSuffix: false }])}>
              Добавить ссылку
            </Button>
            <Button size="sm" icon={<IconBox size={15} />} onClick={() => setPicker(true)}>
              Из хранилища
            </Button>
          </div>
        </div>
        <div className="panel-body stack">
          {form.sources.length === 0 && <p className="muted">Добавьте ссылку, перетащите файл или выберите его из хранилища. Можно обойтись без источников и вписать правила вручную ниже.</p>}
          {form.sources.map((s, i) => (
            <div key={s.id || i} className="source-edit">
              <div className="source-main">
                {s.type === 'url' ? (
                  <Field label={`Ссылка ${i + 1}`}>
                    {(fid) => (
                      <Input
                        id={fid}
                        mono
                        type="url"
                        required
                        value={s.url || ''}
                        onChange={(e) => setSource(i, { url: e.target.value })}
                        placeholder="https://raw.githubusercontent.com/…/list.txt"
                      />
                    )}
                  </Field>
                ) : (
                  <div className="field">
                    <span className="field-label">Файл {i + 1}</span>
                    <div className="src-line">
                      <IconFile size={16} className="muted" aria-hidden />
                      <span className="mono truncate">
                        {s.bucket}/{s.key}
                      </span>
                    </div>
                  </div>
                )}
                <Field label="Формат">
                  {(fid) => (
                    <Select id={fid} value={s.format} onChange={(e) => setSource(i, { format: e.target.value as InputFormat })}>
                      {(Object.keys(INPUT_LABEL) as InputFormat[]).map((f) => (
                        <option key={f} value={f}>
                          {INPUT_LABEL[f]}
                        </option>
                      ))}
                    </Select>
                  )}
                </Field>
                <Button
                  variant="ghost"
                  iconOnly
                  className="source-del"
                  onClick={() => set('sources', form.sources.filter((_, j) => j !== i))}
                  aria-label={`Убрать источник ${i + 1}`}
                  title="Убрать источник"
                  icon={<IconTrash size={16} />}
                />
              </div>
              <Switch
                checked={s.bareAsSuffix}
                onChange={(v) => setSource(i, { bareAsSuffix: v })}
                label="Домены без префикса — вместе с поддоменами"
                hint="Строка example.com будет означать +.example.com. Включите для простых списков доменов; mihomo по умолчанию считает такую строку точным совпадением."
              />
            </div>
          ))}
          <Dropzone
            onFiles={addFiles}
            title={uploading.length ? `Загружается: ${uploading.join(', ')}` : 'Перетащите файлы с компьютера'}
            hint={`или нажмите, чтобы выбрать. MRS, YAML, TXT, LIST, hosts — до ${fmtBytes(me.limits.maxFetch)}`}
          />
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>Обработка</h2>
            <p>Что сделать с правилами перед публикацией</p>
          </div>
        </div>
        <div className="panel-body stack">
          <Field
            label="Behavior"
            hint="domain — только домены, ipcidr — только IP-сети, classical — любые правила вида TYPE,value. При автоопределении выбирается самый узкий подходящий."
          >
            {(fid) => (
              <Select id={fid} value={form.behavior} onChange={(e) => set('behavior', e.target.value as Behavior)} className="select narrow">
                {(Object.keys(BEHAVIOR_LABEL) as Behavior[]).map((b) => (
                  <option key={b} value={b}>
                    {BEHAVIOR_LABEL[b]}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Switch checked={form.optimize} onChange={(v) => set('optimize', v)} label="Убирать перекрытые домены" hint="mail.google.com не нужен, если в наборе есть +.google.com. Поведение набора не меняется, файл становится меньше." />
          <Switch checked={form.aggregateIP} onChange={(v) => set('aggregateIP', v)} label="Склеивать соседние IP-сети" hint="1.0.0.0/24 и 1.0.1.0/24 станут 1.0.0.0/23, вложенные сети пропадут." />
          <Switch checked={form.noResolve} onChange={(v) => set('noResolve', v)} label="Добавлять no-resolve к IP-правилам" hint="Только для файла LIST: mihomo не будет резолвить домен ради проверки IP." />
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>Файлы для клиентов</h2>
            <p>Каждый формат получит свою ссылку</p>
          </div>
        </div>
        <div className="panel-body stack">
          <div className="choices">
            {(Object.keys(FORMAT_INFO) as OutputFormat[]).map((f) => (
              <label key={f} className="choice">
                <input
                  type="checkbox"
                  checked={form.formats.includes(f) && !(f === 'mrs' && mrsBlocked)}
                  disabled={f === 'mrs' && mrsBlocked}
                  onChange={(e) => set('formats', e.target.checked ? [...form.formats, f] : form.formats.filter((x) => x !== f))}
                />
                <span>
                  <b>{FORMAT_INFO[f].title}</b>
                  <small>{f === 'mrs' && mrsBlocked ? 'Недоступен для behavior classical.' : FORMAT_INFO[f].note}</small>
                </span>
              </label>
            ))}
          </div>
          {form.behavior === 'auto' && form.formats.includes('mrs') && (
            <Notice>Если в источниках окажутся и домены, и IP, MRS не соберётся: mihomo хранит их в разных файлах. Тогда создайте два набора или выберите behavior.</Notice>
          )}
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2>Обновление и доступ</h2>
        </div>
        <div className="panel-body form-grid">
          <Field label="Проверять источники" hint="Панель скачает их заново; если что-то изменилось, файлы пересоберутся.">
            {(fid) => (
              <Select id={fid} value={form.intervalMin} onChange={(e) => set('intervalMin', Number(e.target.value))}>
                {INTERVALS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
                {!INTERVALS.some((o) => o.value === form.intervalMin) && <option value={form.intervalMin}>Каждые {form.intervalMin} мин</option>}
              </Select>
            )}
          </Field>
          <div className="field">
            <span className="field-label">Доступ к ссылкам</span>
            <Switch
              checked={form.private}
              onChange={(v) => set('private', v)}
              label="Только по секретному токену"
              hint="Ссылки получат ?token=…, без него сервер ответит 404."
            />
          </div>
          <div className="field">
            <span className="field-label">Уведомления</span>
            <Switch
              checked={form.notify}
              onChange={(v) => set('notify', v)}
              label="Сообщать в Telegram о новых и убранных правилах"
              hint={
                telegram === false
                  ? 'Telegram не настроен: задайте TELEGRAM_TOKEN и ADMIN_ID в .env и перезапустите панель.'
                  : 'Приходит, когда обновление из источников по расписанию или по кнопке что-то изменило.'
              }
            />
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>Ручные правки</h2>
            <p>Сохраняются при каждом обновлении источников</p>
          </div>
        </div>
        <div className="panel-body form-grid">
          <Field label="Добавить" hint="По правилу в строке: example.com — только домен, +.example.com — с поддоменами, 1.2.3.0/24 — сеть.">
            {(fid) => <Textarea id={fid} rows={6} value={include} onChange={(e) => setInclude(e.target.value)} placeholder={'+.example.com\n203.0.113.0/24'} />}
          </Field>
          <div className="stack-s">
            <Field
              label="Исключить"
              hint={
                form.exclude.length
                  ? `Уже исключено правил: ${form.exclude.length}. Их режимы и предпросмотр — во вкладке «Ручные правки» на странице набора.`
                  : 'По правилу в строке. Подробнее с предпросмотром — во вкладке «Ручные правки» на странице набора.'
              }
            >
              {(fid) => <Textarea id={fid} rows={4} value={exclude} onChange={(e) => setExclude(e.target.value)} placeholder={'ads.example.com\n10.0.0.0/8'} />}
            </Field>
            <Field label="Как исключать эти строки">
              {(fid) => (
                <Select id={fid} value={excludeMode} onChange={(e) => setExcludeMode(e.target.value as ExcludeMode)}>
                  {MODES.map((m) => (
                    <option key={m.mode} value={m.mode}>
                      {m.title}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </div>
        </div>
      </section>

      <div className="form-actions">
        <Button type="submit" variant="primary" busy={saving} icon={<IconDeviceFloppy size={17} />}>
          {editing ? 'Сохранить' : 'Создать набор'}
        </Button>
      </div>

      <ObjectPicker
        open={picker}
        onClose={() => setPicker(false)}
        onPick={(bucket, key) => {
          set('sources', [...form.sources, { type: 'object', bucket, key, format: 'auto', bareAsSuffix: false }])
          setPicker(false)
        }}
      />
    </form>
  )
}

const TR: Record<string, string> = {
  а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'e', ж: 'zh', з: 'z', и: 'i', й: 'y', к: 'k', л: 'l', м: 'm',
  н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'h', ц: 'c', ч: 'ch', ш: 'sh', щ: 'sch', ъ: '',
  ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya',
}

/** Mirrors the server's slug rules so the preview matches the final link. */
function translit(name: string) {
  let out = ''
  let dash = false
  for (const ch of name.toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) {
      out += ch
      dash = false
    } else if (ch in TR) {
      out += TR[ch]
      dash = false
    } else if (!dash && out) {
      out += '-'
      dash = true
    }
  }
  return out.replace(/^-+|-+$/g, '').slice(0, 60).replace(/-+$/, '')
}
