import { useEffect, useState } from 'react'
import { IconFile, IconFolder } from '@tabler/icons-react'
import { api } from '../lib/api'
import { fmtBytes, isRuleFile } from '../lib/format'
import { useAsync } from '../lib/hooks'
import type { Bucket, Listing } from '../lib/types'
import { Field, Loading, Notice, Select } from '../ui/core'
import { Modal } from '../ui/overlay'

interface Props {
  open: boolean
  onClose: () => void
  onPick: (bucket: string, key: string) => void
}

/** Lets the user choose a file from storage as a rule source. */
export function ObjectPicker({ open, onClose, onPick }: Props) {
  const buckets = useAsync(() => (open ? api.get<Bucket[]>('buckets') : Promise.resolve([] as Bucket[])), [open])
  const [bucket, setBucket] = useState('')
  const [prefix, setPrefix] = useState('')
  useEffect(() => {
    if (!bucket && buckets.data?.length) setBucket(buckets.data[0].name)
  }, [buckets.data, bucket])
  const listing = useAsync(
    () => (open && bucket ? api.get<Listing>(`buckets/${encodeURIComponent(bucket)}/list?prefix=${encodeURIComponent(prefix)}`) : Promise.resolve(undefined)),
    [open, bucket, prefix],
  )

  const up = prefix ? prefix.slice(0, prefix.slice(0, -1).lastIndexOf('/') + 1) : null

  return (
    <Modal open={open} onClose={onClose} title="Файл из хранилища">
      {buckets.data && buckets.data.length === 0 ? (
        <Notice>В хранилище пока нет бакетов. Создайте бакет в разделе «Хранилище» или перетащите файл прямо в форму.</Notice>
      ) : (
        <div className="stack">
          <Field label="Бакет">
            {(id) => (
              <Select
                id={id}
                value={bucket}
                onChange={(e) => {
                  setBucket(e.target.value)
                  setPrefix('')
                }}
              >
                {buckets.data?.map((b) => (
                  <option key={b.name} value={b.name}>
                    {b.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {!listing.data ? (
            <Loading />
          ) : (
            <div className="entries">
              {up !== null && (
                <button type="button" className="entry pick" onClick={() => setPrefix(up)}>
                  <span className="entry-text">..</span>
                </button>
              )}
              {listing.data.folders.map((f) => (
                <button key={f} type="button" className="entry pick" onClick={() => setPrefix(f)}>
                  <span className="entry-text row-flex">
                    <IconFolder size={15} color="var(--straw)" aria-hidden /> {f.slice(prefix.length)}
                  </span>
                </button>
              ))}
              {listing.data.objects.map((o) => (
                <button key={o.key} type="button" className="entry pick" onClick={() => onPick(o.bucket, o.key)}>
                  <span className={isRuleFile(o.key) ? 'entry-text row-flex' : 'entry-text row-flex muted'}>
                    <IconFile size={15} aria-hidden /> {o.key.slice(prefix.length)}
                  </span>
                  <span className="muted small">{fmtBytes(o.size)}</span>
                </button>
              ))}
              {listing.data.folders.length + listing.data.objects.length === 0 && <div className="entry muted">Пусто</div>}
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}
