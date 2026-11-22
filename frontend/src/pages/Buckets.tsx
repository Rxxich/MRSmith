import { type FormEvent, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { IconBox, IconLock, IconPlus, IconWorld } from '@tabler/icons-react'
import { api, errorText } from '../lib/api'
import { count, dateTime, fmtBytes } from '../lib/format'
import { useAsync, useTitle } from '../lib/hooks'
import type { Bucket } from '../lib/types'
import { Button, Empty, Field, Input, Loading, Notice, Switch } from '../ui/core'
import { Modal, useOverlay } from '../ui/overlay'

export function Buckets() {
  useTitle('Хранилище')
  const list = useAsync(() => api.get<Bucket[]>('buckets'), [])
  const [open, setOpen] = useState(false)

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Хранилище</h1>
          <p>Бакеты для любых файлов: правила, картинки, стили. Файлы из публичного бакета доступны по прямой ссылке вашего домена.</p>
        </div>
        <Button variant="primary" icon={<IconPlus size={17} />} onClick={() => setOpen(true)}>
          Новый бакет
        </Button>
      </div>
      {list.error && <Notice tone="error">{list.error.message}</Notice>}
      {!list.data ? (
        <Loading />
      ) : list.data.length === 0 ? (
        <section className="panel">
          <Empty icon={<IconBox size={30} />} title="Бакетов пока нет" action={<Button variant="primary" icon={<IconPlus size={17} />} onClick={() => setOpen(true)}>Создать бакет</Button>}>
            Бакет — это папка верхнего уровня со своими правами доступа. Например, assets для картинок и стилей или rules для файлов правил.
          </Empty>
        </section>
      ) : (
        <div className="bucket-grid">
          {list.data.map((b) => (
            <Link key={b.name} to={`/storage/${b.name}`} className="panel bucket-card">
              <div className="spread">
                <span className="row-flex">
                  <IconBox size={18} className="muted" aria-hidden />
                  <b>{b.name}</b>
                </span>
                {b.managed ? (
                  <span className="tag tag-muted">ведёт панель</span>
                ) : b.system ? (
                  <span className="tag tag-muted">служебный</span>
                ) : b.public ? (
                  <span className="tag tag-ok">
                    <IconWorld size={12} aria-hidden /> публичный
                  </span>
                ) : (
                  <span className="tag tag-muted">
                    <IconLock size={12} aria-hidden /> приватный
                  </span>
                )}
              </div>
              <span className="dim small">
                {count(b.objects, ['файл', 'файла', 'файлов'])}, {fmtBytes(b.bytes)}
              </span>
              <span className="muted small">
                {b.managed
                  ? 'Скачанные копии ссылок из наборов, по папке на набор'
                  : b.system
                    ? 'Файлы, загруженные с компьютера как источники наборов'
                    : `Создан ${dateTime(b.createdAt)}`}
              </span>
            </Link>
          ))}
        </div>
      )}
      <CreateBucket open={open} onClose={() => setOpen(false)} />
    </div>
  )
}

function CreateBucket({ open, onClose }: { open: boolean; onClose: () => void }) {
  const nav = useNavigate()
  const { toast } = useOverlay()
  const [name, setName] = useState('')
  const [pub, setPub] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const b = await api.post<Bucket>('buckets', { name: name.trim(), public: pub })
      toast(`Бакет ${b.name} создан`)
      nav(`/storage/${b.name}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Новый бакет"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Отмена
          </Button>
          <Button variant="primary" type="submit" form="new-bucket" busy={busy}>
            Создать бакет
          </Button>
        </>
      }
    >
      <form id="new-bucket" className="stack" onSubmit={submit}>
        {error && <Notice tone="error">{error}</Notice>}
        <Field label="Имя" hint="Строчная латиница, цифры и дефис, от 3 до 63 символов. Попадёт в ссылки: /f/имя/файл">
          {(id) => (
            <Input id={id} mono value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="assets" required autoFocus pattern="[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]" />
          )}
        </Field>
        <Switch checked={pub} onChange={setPub} label="Публичный" hint="Файлы открываются по прямой ссылке без входа. Приватные видны только в панели." />
      </form>
    </Modal>
  )
}
