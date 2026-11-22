import { useEffect, useState } from 'react'
import { api, errorText } from '../lib/api'
import { RULES, count } from '../lib/format'
import type { ExcludeMode, Ruleset } from '../lib/types'
import { Button, Loading, Notice, RuleText } from '../ui/core'
import { Modal, useOverlay } from '../ui/overlay'

export const MODE_LABEL: Record<ExcludeMode, string> = {
  exact: 'только эта строка',
  tree: 'с вложенными',
  parents: 'с более широкими',
  cut: 'вырезать адреса',
}

export function ruleKind(rule: string): 'ip' | 'domain' | 'classical' {
  if (rule.includes(',')) return 'classical'
  if (rule.includes('/') || /^[\d.]+$/.test(rule) || (rule.includes(':') && !rule.includes('.'))) return 'ip'
  return 'domain'
}

interface ModeInfo {
  mode: ExcludeMode
  title: string
  hint: (rule: string, kind: 'ip' | 'domain') => string
}

export const MODES: ModeInfo[] = [
  {
    mode: 'exact',
    title: 'Только эта строка',
    hint: () => 'Убирается одно это правило. Остальные не трогаются, даже если покрывают тот же адрес.',
  },
  {
    mode: 'tree',
    title: 'Вместе с вложенными',
    hint: (_r, k) =>
      k === 'ip'
        ? 'Убираются эта сеть и все сети внутри неё.'
        : 'Убираются это правило и всё, что оно покрывает: для +.example.com — link.example.com и другие поддомены.',
  },
  {
    mode: 'parents',
    title: 'Вместе с более широкими',
    hint: (_r, k) =>
      k === 'ip'
        ? 'Убираются эта сеть и все сети, в которые она входит.'
        : 'Убираются это правило и правила, которые его покрывают: для link.example.com — +.example.com.',
  },
  {
    mode: 'cut',
    title: 'Вырезать адреса',
    hint: (_r, k) =>
      k === 'ip'
        ? 'Адреса вырезаются из более крупных сетей: 10.0.0.0/8 без 10.1.0.0/16 превратится в несколько меньших сетей.'
        : 'Адрес перестаёт совпадать, где это можно записать: +.example.com без example.com станет .example.com. Поддомен из +.родителя так не вырезать.',
  },
]

interface Preview {
  removed: string[]
  added: string[]
  removedCount: number
  addedCount: number
  still?: { rule: string; by: string }
}

interface Props {
  rs: Ruleset
  rule: string | null
  onClose: () => void
  onDone: (rs: Ruleset) => void
}

/** Asks how to exclude a rule and shows what would change before applying. */
export function ExcludeDialog({ rs, rule, onClose, onDone }: Props) {
  const { toast } = useOverlay()
  const [mode, setMode] = useState<ExcludeMode>('exact')
  const [preview, setPreview] = useState<Preview | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const kind = rule ? ruleKind(rule) : 'domain'

  useEffect(() => {
    setMode('exact')
  }, [rule])

  useEffect(() => {
    if (!rule) return
    let alive = true
    setPreview(null)
    setError('')
    api
      .post<Preview>(`rulesets/${rs.id}/exclude/preview`, { rule, mode })
      .then((p) => alive && setPreview(p))
      .catch((e) => alive && setError(errorText(e)))
    return () => {
      alive = false
    }
  }, [rs.id, rule, mode])

  const apply = async () => {
    if (!rule) return
    setBusy(true)
    try {
      const updated = await api.post<Ruleset>(`rulesets/${rs.id}/entries`, { remove: [{ rule, mode }] })
      toast(`${rule} исключено (${MODE_LABEL[mode]}). Отменить можно во вкладке «Ручные правки»`)
      onDone(updated)
    } catch (e) {
      toast(errorText(e), 'error')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={!!rule}
      onClose={onClose}
      title={
        <>
          Исключить <span className="mono">{rule}</span>
        </>
      }
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Отмена
          </Button>
          <Button variant="primary" busy={busy} disabled={!preview} onClick={apply}>
            Исключить
          </Button>
        </>
      }
    >
      {rule && (
        <div className="exclude-layout">
          <div className="stack-s">
            {kind === 'classical' ? (
              <Notice>Правило вида TYPE,значение исключается только целиком — режимы к нему не применяются.</Notice>
            ) : (
              MODES.map((m) => (
                <label key={m.mode} className="choice">
                  <input type="radio" name="exclude-mode" checked={mode === m.mode} onChange={() => setMode(m.mode)} />
                  <span>
                    <b>{m.title}</b>
                    <small>{m.hint(rule, kind)}</small>
                  </span>
                </label>
              ))
            )}
          </div>
          <div className="stack-s">
            <span className="field-label">Что изменится</span>
            {error && <Notice tone="error">{error}</Notice>}
            {!preview && !error ? (
              <Loading>Считаю…</Loading>
            ) : preview ? (
              <>
                {preview.removedCount === 0 && preview.addedCount === 0 ? (
                  <p className="muted">
                    Сейчас в наборе нет такой строки. Исключение всё равно сохранится и сработает, если правило придёт из источников.
                  </p>
                ) : (
                  <div className="entries preview-list">
                    {preview.removed.map((r) => (
                      <div key={'-' + r} className="entry">
                        <span className="entry-text minus">
                          − <RuleText rule={r} />
                        </span>
                      </div>
                    ))}
                    {preview.added.map((r) => (
                      <div key={'+' + r} className="entry">
                        <span className="entry-text plus">
                          + <RuleText rule={r} />
                        </span>
                      </div>
                    ))}
                  </div>
                )}
                {(preview.removedCount > preview.removed.length || preview.addedCount > preview.added.length) && (
                  <span className="small muted">
                    Всего уберётся {count(preview.removedCount, RULES)}
                    {preview.addedCount ? `, добавится ${count(preview.addedCount, RULES)}` : ''}.
                  </span>
                )}
                {preview.still && (
                  <Notice tone="warn">
                    Адреса {preview.still.rule} всё равно будут совпадать через <span className="mono">{preview.still.by}</span>. Чтобы убрать и
                    его, выберите «Вместе с более широкими».
                  </Notice>
                )}
              </>
            ) : null}
          </div>
        </div>
      )}
    </Modal>
  )
}
