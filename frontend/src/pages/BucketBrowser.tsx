import { type FormEvent, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  IconArrowsExchange,
  IconCheck,
  IconCloudUpload,
  IconDownload,
  IconFile,
  IconFileCode,
  IconFolder,
  IconFolderPlus,
  IconLayoutGrid,
  IconList,
  IconListDetails,
  IconPencil,
  IconPhoto,
  IconTrash,
  IconX,
} from '@tabler/icons-react'
import { useMe } from '../app/auth'
import { api, errorText, upload } from '../lib/api'
import { count, counterText, counterTitle, dateTime, fileLink, fmtBytes, fmtNum, isImage, isRuleFile, isText } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { Bucket, Listing, Ruleset, StorageObject } from '../lib/types'
import { Button, CopyButton, CopyField, Empty, Field, Input, Loading, Notice, Segmented, Switch } from '../ui/core'
import { useWindowDrop } from '../ui/files'
import { Modal, useOverlay } from '../ui/overlay'

interface Upload {
  id: number
  name: string
  progress: number
  error?: string
  done?: boolean
  abort?: () => void
}

const rawURL = (o: StorageObject) => `api/buckets/${encodeURIComponent(o.bucket)}/object?key=${encodeURIComponent(o.key)}`

let uploadSeq = 0

export function BucketBrowser() {
  const { bucket = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const prefix = params.get('p') || ''
  const me = useMe()
  const nav = useNavigate()
  const { toast, confirm } = useOverlay()
  useTitle(bucket)

  const info = useAsync(() => api.get<Bucket[]>('buckets').then((l) => l.find((b) => b.name === bucket)), [bucket])
  const listing = useAsync(() => api.get<Listing>(`buckets/${encodeURIComponent(bucket)}/list?prefix=${encodeURIComponent(prefix)}`), [bucket, prefix])
  const [view, setView] = useState<'list' | 'grid'>(() => (localStorageGet('mrsmith.view') as 'list' | 'grid') || 'list')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [uploads, setUploads] = useState<Upload[]>([])
  const [preview, setPreview] = useState<StorageObject | null>(null)
  const [renaming, setRenaming] = useState<StorageObject | null>(null)
  const [newFolder, setNewFolder] = useState(false)

  useEffect(() => setSelected(new Set()), [bucket, prefix])
  useEffect(() => localStorageSet('mrsmith.view', view), [view])

  const go = (p: string) => setParams(p ? { p } : {})

  const startUploads = async (files: { file: File; path: string }[]) => {
    const tooBig = files.filter((f) => f.file.size > me.limits.maxUpload)
    tooBig.forEach((f) => toast(`${f.path}: больше ${fmtBytes(me.limits.maxUpload)}`, 'error'))
    const queue = files.filter((f) => f.file.size <= me.limits.maxUpload)
    let ok = 0
    // Upload a few at a time; each request carries one file for clear progress.
    const worker = async () => {
      while (queue.length) {
        const { file, path } = queue.shift()!
        const id = ++uploadSeq
        const fd = new FormData()
        fd.append('prefix', prefix)
        fd.append('path', path)
        fd.append('file', file, file.name)
        const up = upload<StorageObject[]>(`buckets/${encodeURIComponent(bucket)}/upload`, fd, (p) =>
          setUploads((u) => u.map((x) => (x.id === id ? { ...x, progress: p } : x))),
        )
        setUploads((u) => [...u, { id, name: path, progress: 0, abort: up.abort }])
        try {
          await up.promise
          ok++
          setUploads((u) => u.map((x) => (x.id === id ? { ...x, progress: 1, done: true } : x)))
        } catch (e) {
          setUploads((u) => u.map((x) => (x.id === id ? { ...x, error: errorText(e) } : x)))
        }
      }
    }
    await Promise.all([worker(), worker(), worker()])
    if (ok) {
      toast(`Загружено: ${count(ok, ['файл', 'файла', 'файлов'])}`)
      void listing.reload()
      setTimeout(() => setUploads((u) => u.filter((x) => !x.done)), 2500)
    }
  }

  const dragging = useWindowDrop(startUploads, !!info.data && !info.data.managed)
  // Folders of the downloads bucket are named after rule sets.
  const sets = useAsync(() => (bucket === 'downloads' ? api.get<Ruleset[]>('rulesets') : Promise.resolve([] as Ruleset[])), [bucket])

  const del = async (keys: string[], prefixes: string[]) => {
    const n = keys.length + prefixes.length
    const ok = await confirm({
      title: n === 1 ? `Удалить ${(keys[0] || prefixes[0]).replace(prefix, '')}?` : `Удалить ${n} объектов?`,
      body: prefixes.length ? 'Папки удалятся вместе со всем содержимым. Это необратимо.' : 'Файлы удалятся безвозвратно, публичные ссылки перестанут работать.',
      confirm: 'Удалить',
      danger: true,
    })
    if (!ok) return
    try {
      const r = await api.post<{ deleted: number }>(`buckets/${encodeURIComponent(bucket)}/delete`, { keys, prefixes })
      toast(`Удалено: ${count(r.deleted, ['файл', 'файла', 'файлов'])}`)
      setSelected(new Set())
      void listing.reload()
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const togglePublic = async (v: boolean) => {
    try {
      const b = await api.patch<Bucket>(`buckets/${encodeURIComponent(bucket)}`, { public: v })
      info.setData(b)
      toast(v ? 'Бакет стал публичным' : 'Бакет стал приватным')
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  const deleteBucket = async () => {
    const ok = await confirm({
      title: `Удалить бакет ${bucket}?`,
      body: listing.data?.total ? `Вместе с ним удалятся ${count(listing.data.total, ['файл', 'файла', 'файлов'])}. Это необратимо.` : 'Бакет пуст.',
      confirm: 'Удалить бакет',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`buckets/${encodeURIComponent(bucket)}?force=1`)
      toast(`Бакет ${bucket} удалён`)
      nav('/storage')
    } catch (e) {
      toast(errorText(e), 'error')
    }
  }

  if (info.error) return <Notice tone="error">{info.error.message}</Notice>
  if (info.data === undefined && info.loading) return <Loading />
  if (!info.data) return <Notice tone="error">Бакет {bucket} не найден.</Notice>
  const b = info.data
  const ro = !!b.managed
  const owner = (folder: string) => sets.data?.find((r) => r.slug === folder.split('/')[0])
  const l = listing.data
  const crumbs = prefix.split('/').filter(Boolean)
  const allKeys = l ? [...l.folders, ...l.objects.map((o) => o.key)] : []
  const toggle = (k: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(k)) n.delete(k)
      else n.add(k)
      return n
    })

  return (
    <div className="stack">
      <div className="page-head">
        <div className="grow">
          <div className="crumbs">
            <Link to="/storage">Хранилище</Link>
            <span aria-hidden>/</span>
            {crumbs.length ? <a href="#" onClick={(e) => (e.preventDefault(), go(''))}>{bucket}</a> : <span>{bucket}</span>}
            {crumbs.map((c, i) => {
              const p = crumbs.slice(0, i + 1).join('/') + '/'
              return (
                <span key={p} className="crumb-part">
                  <span aria-hidden>/</span>
                  {i === crumbs.length - 1 ? <span>{c}</span> : <a href="#" onClick={(e) => (e.preventDefault(), go(p))}>{c}</a>}
                </span>
              )
            })}
          </div>
          <h1>{crumbs.length ? crumbs[crumbs.length - 1] : bucket}</h1>
        </div>
        <div className="row-flex">
          {!b.system && <Switch checked={b.public} onChange={togglePublic} label="Публичный" />}
          {!ro && (
            <>
              <Button icon={<IconFolderPlus size={17} />} onClick={() => setNewFolder(true)}>
                Папка
              </Button>
              <label className="btn btn-primary">
                <IconCloudUpload size={17} /> Загрузить
                <input
                  type="file"
                  multiple
                  hidden
                  onChange={(e) => {
                    const files = Array.from(e.target.files || []).map((f) => ({ file: f, path: f.name }))
                    e.target.value = ''
                    if (files.length) void startUploads(files)
                  }}
                />
              </label>
            </>
          )}
        </div>
      </div>

      {b.managed && (
        <Notice>
          Здесь лежат последние скачанные копии ссылок из наборов правил — по папке на каждый набор. Панель обновляет файл при каждой
          проверке ссылки, удаляет его, когда вы меняете или убираете ссылку, переносит папку при переименовании набора и удаляет её вместе
          с набором. Менять файлы здесь нельзя — только смотреть и скачивать.
        </Notice>
      )}
      {b.name === 'rule-sources' && (
        <Notice>
          Сюда попадают файлы, загруженные с компьютера как источники наборов. Файл удаляется сам, когда его больше не использует ни один
          набор. Если удалить его вручную, источник набора перестанет загружаться.
        </Notice>
      )}

      {uploads.length > 0 && (
        <section className="panel">
          <div className="panel-head">
            <h2>Загрузка</h2>
            <Button size="sm" variant="ghost" onClick={() => setUploads((u) => u.filter((x) => !x.done && !x.error))}>
              Очистить
            </Button>
          </div>
          <div className="panel-body stack-s">
            {uploads.map((u) => (
              <div key={u.id} className="upload-row">
                <span className="truncate mono small">{u.name}</span>
                {u.error ? (
                  <span className="small text-error">{u.error}</span>
                ) : u.done ? (
                  <IconCheck size={16} color="var(--patina)" aria-label="загружен" />
                ) : (
                  <>
                    <div className="progress">
                      <i style={{ width: `${Math.round(u.progress * 100)}%` }} />
                    </div>
                    <Button size="sm" variant="ghost" iconOnly onClick={u.abort} aria-label="Отменить" icon={<IconX size={14} />} />
                  </>
                )}
              </div>
            ))}
          </div>
        </section>
      )}

      <section className="panel">
        <div className="panel-head">
          <span className="muted small">
            {l ? `${count(l.folders.length, ['папка', 'папки', 'папок'])}, ${count(l.objects.length, ['файл', 'файла', 'файлов'])}` : ''}
            {b.public ? '' : '. Бакет приватный: файлы видны только в панели'}
          </span>
          <div className="row-flex">
            {selected.size > 0 && !ro && (
              <Button
                size="sm"
                variant="danger"
                icon={<IconTrash size={15} />}
                onClick={() =>
                  del(
                    [...selected].filter((k) => !k.endsWith('/')),
                    [...selected].filter((k) => k.endsWith('/')),
                  )
                }
              >
                Удалить выбранные: {selected.size}
              </Button>
            )}
            <Segmented
              label="Вид"
              value={view}
              onChange={setView}
              options={[
                { value: 'list', label: <span className="sr-only">Список</span>, icon: <IconList size={16} /> },
                { value: 'grid', label: <span className="sr-only">Плитка</span>, icon: <IconLayoutGrid size={16} /> },
              ]}
            />
          </div>
        </div>

        {!l ? (
          <Loading />
        ) : l.folders.length + l.objects.length === 0 ? (
          <Empty icon={<IconCloudUpload size={30} />} title={prefix ? 'Папка пуста' : 'Бакет пуст'}>
            {ro
              ? 'Файлы появятся после первой проверки ссылок в наборах правил.'
              : 'Перетащите файлы или целые папки в это окно — они загрузятся сюда. Картинки PNG, JPEG, SVG можно будет посмотреть прямо в панели.'}
          </Empty>
        ) : view === 'list' ? (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th className="check">
                    <input
                      hidden={ro}
                      type="checkbox"
                      aria-label="Выбрать все"
                      checked={allKeys.length > 0 && selected.size === allKeys.length}
                      onChange={(e) => setSelected(e.target.checked ? new Set(allKeys) : new Set())}
                    />
                  </th>
                  <th>Имя</th>
                  <th className="num">Размер</th>
                  {b.public && <th className="num">Скачиваний</th>}
                  <th>Изменён</th>
                  <th className="actions" />
                </tr>
              </thead>
              <tbody>
                {l.folders.map((f) => (
                  <tr key={f}>
                    <td className="check">
                      <input type="checkbox" hidden={ro} aria-label={`Выбрать ${f}`} checked={selected.has(f)} onChange={() => toggle(f)} />
                    </td>
                    <td>
                      <div className="name-cell">
                        <span className="file-icon is-folder">
                          <IconFolder size={17} />
                        </span>
                        <button type="button" onClick={() => go(f)} className="truncate">
                          {f.slice(prefix.length, -1)}
                        </button>
                        {owner(f) && (
                          <Link to={`/rulesets/${owner(f)!.id}`} className="small folder-owner">
                            набор «{owner(f)!.name}»
                          </Link>
                        )}
                      </div>
                    </td>
                    <td className="num muted">—</td>
                    {b.public && <td className="num muted">—</td>}
                    <td className="muted">—</td>
                    <td className="actions">
                      {!ro && <Button size="sm" variant="ghost" iconOnly onClick={() => del([], [f])} aria-label={`Удалить папку ${f}`} title="Удалить папку" icon={<IconTrash size={15} />} />}
                    </td>
                  </tr>
                ))}
                {l.objects.map((o) => (
                  <tr key={o.key}>
                    <td className="check">
                      <input type="checkbox" hidden={ro} aria-label={`Выбрать ${o.key}`} checked={selected.has(o.key)} onChange={() => toggle(o.key)} />
                    </td>
                    <td>
                      <div className="name-cell">
                        <FileIcon o={o} />
                        <button type="button" onClick={() => setPreview(o)} className="truncate" title={o.key}>
                          {o.key.slice(prefix.length)}
                        </button>
                      </div>
                    </td>
                    <td className="num muted">{fmtBytes(o.size)}</td>
                    {b.public && (
                      <td className="num muted" title={counterTitle(o.downloads)}>
                        {o.downloads?.downloads || o.downloads?.checks ? fmtNum(o.downloads.downloads) : '—'}
                      </td>
                    )}
                    <td className="muted nowrap">{dateTime(o.updatedAt)}</td>
                    <td className="actions">
                      <ObjectActions o={o} isPublic={b.public} readOnly={ro} onRename={() => setRenaming(o)} onDelete={() => del([o.key], [])} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="panel-body tiles">
            {l.folders.map((f) => (
              <button key={f} type="button" className="tile" onClick={() => go(f)}>
                <div className="tile-preview is-folder">
                  <IconFolder size={34} stroke={1.4} />
                </div>
                <div className="tile-info">
                  <span className="tile-name">{f.slice(prefix.length, -1)}</span>
                  <span className="muted">папка</span>
                </div>
              </button>
            ))}
            {l.objects.map((o) => (
              <button key={o.key} type="button" className={selected.has(o.key) ? 'tile is-selected' : 'tile'} onClick={() => setPreview(o)}>
                <div className={isImage(o.contentType) ? 'tile-preview' : 'tile-preview is-file'}>
                  {isImage(o.contentType) ? <img src={rawURL(o)} alt="" loading="lazy" /> : isRuleFile(o.key) ? <IconFileCode size={34} stroke={1.4} color="var(--straw)" /> : <IconFile size={34} stroke={1.4} />}
                </div>
                <div className="tile-info">
                  <span className="tile-name" title={o.key}>
                    {o.key.slice(prefix.length)}
                  </span>
                  <span className="muted">{fmtBytes(o.size)}</span>
                </div>
              </button>
            ))}
          </div>
        )}
      </section>

      {!b.system && !ro && (
        <div className="form-actions">
          <Button variant="danger" size="sm" icon={<IconTrash size={15} />} onClick={deleteBucket}>
            Удалить бакет
          </Button>
        </div>
      )}

      {dragging && (
        <div className="drop-overlay">
          <div>
            <IconCloudUpload size={44} />
            Отпустите, чтобы загрузить в {bucket}/{prefix}
          </div>
        </div>
      )}

      <Preview o={preview} isPublic={b.public} readOnly={ro} onClose={() => setPreview(null)} onDelete={(o) => (setPreview(null), del([o.key], []))} />
      <Rename
        o={renaming}
        onClose={() => setRenaming(null)}
        onDone={() => {
          setRenaming(null)
          void listing.reload()
        }}
      />
      <NewFolder
        open={newFolder}
        onClose={() => setNewFolder(false)}
        onCreate={(name) => {
          setNewFolder(false)
          go(prefix + name.replace(/^\/+|\/+$/g, '') + '/')
        }}
      />
    </div>
  )
}

function FileIcon({ o }: { o: StorageObject }) {
  if (isImage(o.contentType)) {
    return (
      <span className="file-icon">
        <img src={rawURL(o)} alt="" loading="lazy" />
      </span>
    )
  }
  if (isRuleFile(o.key))
    return (
      <span className="file-icon is-rules">
        <IconFileCode size={17} />
      </span>
    )
  return <span className="file-icon">{o.contentType.startsWith('image') ? <IconPhoto size={17} /> : <IconFile size={17} />}</span>
}

function ObjectActions({ o, isPublic, readOnly, onRename, onDelete }: { o: StorageObject; isPublic: boolean; readOnly: boolean; onRename: () => void; onDelete: () => void }) {
  const me = useMe()
  return (
    <div className="obj-actions">
      {isPublic && <CopyButton text={fileLink(me, o.bucket, o.key)} label="Скопировать публичную ссылку" />}
      {isRuleFile(o.key) && (
        <Link className="btn btn-ghost btn-sm btn-icon" to={`/convert?bucket=${encodeURIComponent(o.bucket)}&key=${encodeURIComponent(o.key)}`} title="Открыть в конвертере" aria-label="Открыть в конвертере">
          <IconArrowsExchange size={15} />
        </Link>
      )}
      <a className="btn btn-ghost btn-sm btn-icon" href={rawURL(o) + '&download=1'} title="Скачать" aria-label={`Скачать ${o.key}`}>
        <IconDownload size={15} />
      </a>
      {!readOnly && (
        <>
          <Button size="sm" variant="ghost" iconOnly onClick={onRename} title="Переименовать" aria-label={`Переименовать ${o.key}`} icon={<IconPencil size={15} />} />
          <Button size="sm" variant="ghost" iconOnly onClick={onDelete} title="Удалить" aria-label={`Удалить ${o.key}`} icon={<IconTrash size={15} />} />
        </>
      )}
    </div>
  )
}

function Preview({ o, isPublic, readOnly, onClose, onDelete }: { o: StorageObject | null; isPublic: boolean; readOnly: boolean; onClose: () => void; onDelete: (o: StorageObject) => void }) {
  const me = useMe()
  const [text, setText] = useState<string | null>(null)
  useEffect(() => {
    setText(null)
    if (!o || !isText(o.contentType) || isImage(o.contentType)) return
    if (o.size > 512 * 1024) {
      setText('Файл больше 512 КБ — скачайте его, чтобы посмотреть целиком.')
      return
    }
    let alive = true
    fetch(rawURL(o), { credentials: 'same-origin' })
      .then((r) => r.text())
      .then((t) => alive && setText(t))
      .catch(() => alive && setText('Не удалось загрузить содержимое.'))
    return () => {
      alive = false
    }
  }, [o, isPublic])

  return (
    <Modal
      open={!!o}
      onClose={onClose}
      title={o?.key.split('/').pop()}
      wide
      footer={
        o && (
          <>
            {!readOnly && (
              <Button variant="danger" icon={<IconTrash size={16} />} onClick={() => onDelete(o)}>
                Удалить
              </Button>
            )}
            <span className="grow" />
            {isRuleFile(o.key) && (
              <>
                <Link className="btn btn-ghost" to={`/rulesets/new?bucket=${encodeURIComponent(o.bucket)}&key=${encodeURIComponent(o.key)}`}>
                  <IconListDetails size={16} /> Набор из файла
                </Link>
                <Link className="btn btn-ghost" to={`/convert?bucket=${encodeURIComponent(o.bucket)}&key=${encodeURIComponent(o.key)}`}>
                  <IconArrowsExchange size={16} /> Конвертировать
                </Link>
              </>
            )}
            <a className="btn btn-primary" href={rawURL(o) + '&download=1'}>
              <IconDownload size={16} /> Скачать
            </a>
          </>
        )
      }
    >
      {o && (
        <div className="stack">
          {isImage(o.contentType) ? (
            <div className="preview-stage">
              <img src={rawURL(o)} alt={o.key} />
            </div>
          ) : text !== null ? (
            <pre className="code">{text}</pre>
          ) : !isText(o.contentType) ? (
            <p className="muted">Для этого типа файла нет предпросмотра.</p>
          ) : (
            <Loading />
          )}
          <dl className="facts narrow">
            <dt>Путь</dt>
            <dd className="mono">
              {o.bucket}/{o.key}
            </dd>
            {o.origin && (
              <>
                <dt>Скачан с</dt>
                <dd className="mono truncate" title={o.origin}>
                  {o.origin}
                </dd>
              </>
            )}
            <dt>Тип</dt>
            <dd>{o.contentType}</dd>
            <dt>Размер</dt>
            <dd>{fmtBytes(o.size)}</dd>
            <dt>Изменён</dt>
            <dd>{dateTime(o.updatedAt)}</dd>
            {isPublic && (
              <>
                <dt>По ссылке</dt>
                <dd title={counterTitle(o.downloads)}>{counterText(o.downloads)}</dd>
              </>
            )}
            <dt>SHA-256</dt>
            <dd className="mono truncate" title={o.sha256}>
              {o.sha256.slice(0, 16)}…
            </dd>
          </dl>
          {isPublic ? <CopyField value={fileLink(me, o.bucket, o.key)} /> : <span className="small muted">Бакет приватный — публичной ссылки нет.</span>}
        </div>
      )}
    </Modal>
  )
}

function Rename({ o, onClose, onDone }: { o: StorageObject | null; onClose: () => void; onDone: () => void }) {
  const { toast } = useOverlay()
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => setTo(o?.key || ''), [o])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!o) return
    setBusy(true)
    try {
      await api.post(`buckets/${encodeURIComponent(o.bucket)}/rename`, { from: o.key, to: to.trim() })
      toast('Файл переименован')
      onDone()
    } catch (err) {
      toast(errorText(err), 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={!!o}
      onClose={onClose}
      title="Переименовать"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Отмена
          </Button>
          <Button variant="primary" type="submit" form="rename" busy={busy}>
            Переименовать
          </Button>
        </>
      }
    >
      <form id="rename" onSubmit={submit}>
        <Field label="Новый путь" hint="Через / можно перенести файл в другую папку. Публичная ссылка изменится.">
          {(id) => <Input id={id} mono value={to} onChange={(e) => setTo(e.target.value)} autoFocus />}
        </Field>
      </form>
    </Modal>
  )
}

function NewFolder({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (name: string) => void }) {
  const [name, setName] = useState('')
  useEffect(() => setName(''), [open])
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Новая папка"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Отмена
          </Button>
          <Button variant="primary" type="submit" form="new-folder" disabled={!name.trim() || name.includes('..')}>
            Открыть папку
          </Button>
        </>
      }
    >
      <form
        id="new-folder"
        onSubmit={(e) => {
          e.preventDefault()
          onCreate(name.trim())
        }}
      >
        <Field label="Имя папки" hint="Папка появится в списке, когда в неё будет загружен первый файл — как в S3.">
          {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="images" />}
        </Field>
      </form>
    </Modal>
  )
}

function localStorageGet(k: string): string | null {
  try {
    return window.localStorage.getItem(k)
  } catch {
    return null
  }
}

function localStorageSet(k: string, v: string) {
  try {
    window.localStorage.setItem(k, v)
  } catch {
    /* storage may be unavailable */
  }
}
