import { useRef, useState } from 'react'
import { IconArchive, IconDownload, IconFolderSearch, IconRestore, IconTrash, IconUpload } from '@tabler/icons-react'
import { useMe } from '../app/auth'
import { api, errorText, upload } from '../lib/api'
import { count, dateTime, fmtBytes, publicBase, relTime } from '../lib/format'
import { useAsync, useInterval, useNow } from '../lib/hooks'
import type { BackupFile, BackupJob, BackupSettings, BackupStatus } from '../lib/types'
import { Button, Field, Loading, Notice, Select, Switch } from '../ui/core'
import { useOverlay } from '../ui/overlay'

const EVERY: { value: number; label: string }[] = [
  { value: 0, label: 'Выключено' },
  { value: 12, label: 'Раз в 12 часов' },
  { value: 24, label: 'Раз в день' },
  { value: 72, label: 'Раз в 3 дня' },
  { value: 168, label: 'Раз в неделю' },
]
const KEEP = [3, 5, 7, 10]
const SETS: [string, string, string] = ['набор правил', 'набора правил', 'наборов правил']
const FILES: [string, string, string] = ['файл', 'файла', 'файлов']

function contents(m: { rulesets: number; objects: number }) {
  return `${count(m.rulesets, SETS)}, ${count(m.objects, FILES)} в хранилище`
}

export function BackupsPanel() {
  const me = useMe()
  const base = publicBase(me)
  const { toast, confirm } = useOverlay()
  const status = useAsync(() => api.get<BackupStatus>('backups'), [])
  const [saving, setSaving] = useState(false)
  const [progress, setProgress] = useState<number | null>(null)
  const [hidden, setHidden] = useState('')
  const picker = useRef<HTMLInputElement>(null)
  useNow()

  const st = status.data
  const running = st?.job ?? null
  useInterval(() => void status.reload(), 1500, !!running)
  // Pick up an automatic backup that is about to be made.
  const dueSoon = !!st?.settings.everyHours && !!st.nextAutoAt && new Date(st.nextAutoAt).getTime() - Date.now() < 90_000
  useInterval(() => void status.reload(), 20_000, dueSoon && !running)

  const save = async (next: BackupSettings) => {
    setSaving(true)
    try {
      status.setData(await api.put<BackupStatus>('backups/settings', { ...next, linkBase: base }))
      toast('Настройки бэкапов сохранены')
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setSaving(false)
    }
  }

  const create = async () => {
    try {
      status.setData(await api.post<BackupStatus>('backups', { linkBase: base }))
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const restore = async (f: BackupFile) => {
    const m = f.manifest!
    const moved = !!m.linkBase && m.linkBase !== base
    const ok = await confirm({
      title: 'Восстановить из бэкапа?',
      body: (
        <div className="stack-s">
          <span>
            Наборы правил и файлы хранилища заменятся содержимым бэкапа от {dateTime(m.createdAt)}. В нём {contents(m)}.
          </span>
          {m.names && m.names.length > 0 && <span className="small">Наборы: {m.names.join(', ')}.</span>}
          <span>Перед этим панель сохранит нынешние данные в отдельный бэкап, к ним можно будет вернуться. Логин, пароль и журнал скачиваний не меняются.</span>
          {moved && (
            <span>
              Бэкап сделан на {m.linkBase}. Здесь ссылки будут начинаться с {base}. Имена файлов и токены приватных наборов останутся
              прежними, поэтому в конфигах mihomo нужно заменить только адрес.
            </span>
          )}
        </div>
      ),
      confirm: 'Восстановить',
      danger: true,
    })
    if (!ok) return
    try {
      status.setData(await api.post<BackupStatus>('backups/restore', { name: f.name, linkBase: base }))
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const remove = async (f: BackupFile) => {
    const ok = await confirm({
      title: 'Удалить бэкап?',
      body: `Файл ${f.name} удалится из папки backup на сервере. Вернуть его будет нельзя.`,
      confirm: 'Удалить',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`backups/files/${encodeURIComponent(f.name)}`)
      toast('Бэкап удалён')
      void status.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const send = async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    setProgress(0)
    try {
      const saved = await upload<BackupFile>('backups/upload', form, setProgress).promise
      toast(`Архив ${saved.name} в папке, его можно восстановить`)
      void status.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setProgress(null)
    }
  }

  const check = async () => {
    await status.reload()
    toast('Папка проверена')
  }

  return (
    <section className="panel" id="backups">
      <div className="panel-head">
        <div>
          <h2>Бэкапы</h2>
          <p>Наборы правил вместе с ручными правками и все файлы хранилища. Логин, пароль, сессии и журнал скачиваний в бэкап не входят.</p>
        </div>
        {st && (
          <Button size="sm" variant="ghost" icon={<IconFolderSearch size={15} />} onClick={check} disabled={status.loading}>
            Проверить папку
          </Button>
        )}
      </div>
      {status.error ? (
        <div className="panel-body">
          <Notice tone="error">{status.error.message}</Notice>
        </div>
      ) : !st ? (
        <Loading />
      ) : (
        <>
          <div className="panel-body stack">
            {!st.ready && st.problem && <Notice tone="warn">{st.problem}</Notice>}
            {running && <JobNotice job={running} />}
            {!running && st.last && st.last.finishedAt !== hidden && (
              <LastNotice job={st.last} onHide={() => setHidden(st.last?.finishedAt || '')} />
            )}
            <div className="backup-plan">
              <Field label="Создавать автоматически">
                {(id) => (
                  <Select id={id} value={st.settings.everyHours} disabled={saving} onChange={(e) => save({ ...st.settings, everyHours: Number(e.target.value) })}>
                    {EVERY.map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="Хранить автоматических" hint="Более старые удаляются сами">
                {(id) => (
                  <Select id={id} value={st.settings.keep} disabled={saving} onChange={(e) => save({ ...st.settings, keep: Number(e.target.value) })}>
                    {[...new Set([...KEEP, st.settings.keep])]
                      .sort((a, b) => a - b)
                      .map((n) => (
                        <option key={n} value={n}>
                          {count(n, ['последний', 'последних', 'последних'])}
                        </option>
                      ))}
                  </Select>
                )}
              </Field>
              <div className="backup-switch">
                <Switch
                  checked={st.settings.telegram}
                  disabled={saving || !st.telegram}
                  onChange={(v) => save({ ...st.settings, telegram: v })}
                  label="Отправлять в Telegram"
                  hint={st.telegram ? 'Каждый новый бэкап, до 50 МБ' : 'Сначала задайте TELEGRAM_TOKEN и ADMIN_ID в .env'}
                />
              </div>
            </div>
            {st.settings.everyHours > 0 && <Schedule st={st} />}
            {st.lastError && <Notice tone="warn">{st.lastError}</Notice>}
            <p className="small muted">
              Архивы лежат в папке backup рядом с docker-compose.yml. Чтобы перенести панель на другой сервер, положите архив в такую же
              папку там или загрузите его кнопкой ниже, затем нажмите «Восстановить».
            </p>
          </div>
          <div className="panel-foot backup-actions">
            <input
              ref={picker}
              type="file"
              hidden
              accept=".zip,application/zip"
              onChange={(e) => {
                const f = e.target.files?.[0]
                e.target.value = ''
                if (f) void send(f)
              }}
            />
            <Button
              variant="secondary"
              icon={<IconUpload size={16} />}
              busy={progress !== null}
              disabled={!st.ready}
              onClick={() => picker.current?.click()}
            >
              {progress !== null ? `Загружаю ${Math.round(progress * 100)}%` : 'Загрузить архив'}
            </Button>
            <Button variant="primary" icon={<IconArchive size={17} />} busy={running?.op === 'create'} disabled={!st.ready || !!running} onClick={create}>
              Создать бэкап
            </Button>
          </div>
          <FileList files={st.files} running={running} onRestore={restore} onDelete={remove} />
        </>
      )}
    </section>
  )
}

/** The temper strip again: it cools from the last automatic backup to the
 *  next one. */
function Schedule({ st }: { st: BackupStatus }) {
  const every = st.settings.everyHours * 3600_000
  const last = st.lastAutoAt ? new Date(st.lastAutoAt).getTime() : 0
  const f = last ? Math.min(1, Math.max(0, (Date.now() - last) / every)) : null
  const next = st.nextAutoAt && new Date(st.nextAutoAt).getTime() > Date.now() + 45_000 ? relTime(st.nextAutoAt) : 'в течение минуты'
  return (
    <div>
      <div className={f === null ? 'temper is-manual' : 'temper'} role="img" aria-label={`Следующий автоматический бэкап ${next}`}>
        {f !== null && <span className="temper-cover" style={{ width: `${(1 - Math.max(f, 0.03)) * 100}%` }} />}
      </div>
      <div className="temper-legend">
        <span>{last ? `Последний автоматический ${relTime(st.lastAutoAt)}` : 'Автоматических бэкапов ещё не было'}</span>
        <span>Следующий {next}</span>
      </div>
    </div>
  )
}

function JobNotice({ job }: { job: BackupJob }) {
  return (
    <Notice>
      {job.op === 'restore'
        ? `Восстанавливаю из ${job.name}. Сначала панель сохраняет нынешние данные, затем заменяет их.`
        : job.kind === 'auto'
          ? 'Создаю автоматический бэкап…'
          : 'Создаю бэкап…'}
    </Notice>
  )
}

function LastNotice({ job, onHide }: { job: BackupJob; onHide: () => void }) {
  const hide = (
    <button type="button" className="link-btn" onClick={onHide}>
      Скрыть
    </button>
  )
  if (job.error) {
    return (
      <Notice tone="error">
        {job.op === 'restore' ? `Восстановить из ${job.name} не получилось, данные остались прежними: ` : 'Бэкап не создан: '}
        {job.error} {hide}
      </Notice>
    )
  }
  if (job.op === 'create') {
    return (
      <Notice tone={job.warning ? 'warn' : 'info'}>
        {job.warning || `Бэкап ${job.name} создан.`} {hide}
      </Notice>
    )
  }
  const r = job.restore!
  const moved = r.oldBase && r.newBase && r.oldBase !== r.newBase
  return (
    <Notice>
      <div className="stack-s">
        <span>
          Восстановлено из {job.name}, в нём {contents(r)}. Наборы собираются заново, это займёт до минуты.
          {r.safety && ` Прежние данные сохранены в ${r.safety}.`}
        </span>
        {moved && (
          <span>
            Адрес ссылок сменился: был {r.oldBase}, теперь {r.newBase}. Замените его в конфигах mihomo, остальная часть ссылок прежняя.
            {r.rebased > 0 && ` ${count(r.rebased, ['источник ссылался', 'источника ссылались', 'источников ссылались'])} на старый адрес панели, панель их переписала.`}
          </span>
        )}
        {!r.oldBase && <span>В бэкапе нет адреса старой панели. Проверьте, с какого адреса mihomo скачивает ссылки.</span>}
        <span>{hide}</span>
      </div>
    </Notice>
  )
}

const KIND: Record<string, { label: string; cls: string }> = {
  auto: { label: 'автоматический', cls: 'tag tag-muted' },
  manual: { label: 'вручную', cls: 'tag tag-muted' },
  'before-restore': { label: 'до восстановления', cls: 'tag tag-warn' },
}

interface ListProps {
  files: BackupFile[]
  running: BackupJob | null
  onRestore: (f: BackupFile) => void
  onDelete: (f: BackupFile) => void
}

function FileList({ files, running, onRestore, onDelete }: ListProps) {
  if (files.length === 0) {
    return <div className="panel-body muted backup-empty">В папке пока нет бэкапов.</div>
  }
  return (
    <div className="backup-list">
      {files.map((f) => {
        const m = f.manifest
        const kind = m && KIND[m.kind]
        return (
          <div key={f.name} className="backup-row">
            <div className="backup-what">
              <div className="row-flex">
                <b>{m ? dateTime(m.createdAt) : dateTime(f.modTime)}</b>
                {f.foreign ? <span className="tag tag-domain">с другого сервера</span> : kind && <span className={kind.cls}>{kind.label}</span>}
              </div>
              <span className="mono-soft small truncate" title={f.name}>
                {f.name}
              </span>
            </div>
            <div className="backup-inside">
              {m ? (
                <>
                  <span>{contents(m)}</span>
                  {m.linkBase && <span className="small muted truncate">{m.linkBase}</span>}
                </>
              ) : (
                <span className="small text-error">{f.error}</span>
              )}
            </div>
            <span className="backup-size muted nowrap">{fmtBytes(f.size)}</span>
            <div className="backup-row-actions">
              <a className="btn btn-ghost btn-sm" href={`api/backups/files/${encodeURIComponent(f.name)}`} download={f.name}>
                <IconDownload size={15} aria-hidden />
                Скачать
              </a>
              {m && (
                <Button size="sm" icon={<IconRestore size={15} />} disabled={!!running} onClick={() => onRestore(f)}>
                  Восстановить
                </Button>
              )}
              <Button size="sm" variant="ghost" iconOnly aria-label={`Удалить ${f.name}`} title="Удалить" disabled={running?.name === f.name} onClick={() => onDelete(f)}>
                <IconTrash size={15} />
              </Button>
            </div>
          </div>
        )
      })}
    </div>
  )
}
