import type { Behavior, Counter, InputFormat, Me, OutputFormat, Ruleset } from './types'

const pluralRules = new Intl.PluralRules('ru')

/** plural(5, ['правило', 'правила', 'правил']) → "правил" */
export function plural(n: number, forms: [string, string, string]): string {
  const rule = pluralRules.select(n)
  if (rule === 'one') return forms[0]
  if (rule === 'few') return forms[1]
  return forms[2]
}

const num = new Intl.NumberFormat('ru')
export const fmtNum = (n: number) => num.format(n)

export function count(n: number, forms: [string, string, string]): string {
  return `${fmtNum(n)} ${plural(n, forms)}`
}

export const RULES: [string, string, string] = ['правило', 'правила', 'правил']
export const DOWNLOADS: [string, string, string] = ['скачивание', 'скачивания', 'скачиваний']
export const CHECKS: [string, string, string] = ['проверка', 'проверки', 'проверок']

/** Sums the counters of every published format of a rule set. */
export function rulesetDownloads(rs: Ruleset): Counter {
  const total: Counter = { downloads: 0, checks: 0 }
  for (const c of Object.values(rs.downloads || {})) {
    if (!c) continue
    total.downloads += c.downloads
    total.checks += c.checks
    if (c.lastAt && (!total.lastAt || c.lastAt > total.lastAt)) total.lastAt = c.lastAt
  }
  return total
}

/** One line for a counter: "12 скачиваний, 40 проверок". */
export function counterText(c?: Counter): string {
  if (!c || (!c.downloads && !c.checks)) return 'ещё не скачивали'
  const parts = [count(c.downloads, DOWNLOADS)]
  if (c.checks) parts.push(count(c.checks, CHECKS))
  return parts.join(', ')
}

const CHECK_HINT = 'Проверка: клиент спросил, нет ли новой версии, и получил ответ «не изменилось» без скачивания файла.'

export function counterTitle(c?: Counter): string {
  if (!c || (!c.downloads && !c.checks)) return 'По публичной ссылке файл ещё не запрашивали'
  return `${counterText(c)}. Последний запрос ${relTime(c.lastAt)}.${c.checks ? ' ' + CHECK_HINT : ''}`
}

const APPS: [RegExp, string][] = [
  [/mihomo/i, 'mihomo'],
  [/clash[-. ]?verge/i, 'Clash Verge'],
  [/flclash/i, 'FlClash'],
  [/clashx/i, 'ClashX'],
  [/clash[-. ]?meta/i, 'Clash Meta'],
  [/clash[-. ]?mi/i, 'Clash Mi'],
  [/stash/i, 'Stash'],
  [/sing-?box/i, 'sing-box'],
  [/hiddify/i, 'Hiddify'],
  [/nekobox|nekoray/i, 'NekoBox'],
  [/v2rayn/i, 'v2rayN'],
  [/karing/i, 'Karing'],
  [/koala/i, 'Koala Clash'],
  [/clash/i, 'Clash'],
  [/^curl\//i, 'curl'],
  [/^wget/i, 'Wget'],
  [/go-http-client/i, 'Go-клиент'],
]

/** A short name for the program behind a User-Agent. */
export function describeAgent(ua: string): string {
  if (!ua) return 'Без User-Agent'
  for (const [re, name] of APPS) {
    if (re.test(ua)) {
      const v = new RegExp(re.source + '[^/\\s]*/v?([0-9][\\w.\\-]*)', 'i').exec(ua)
      return v ? `${name} ${v[1]}` : name
    }
  }
  if (/Mozilla\//.test(ua)) return describeBrowser(ua)
  return ua.split(/[\s(]/)[0].slice(0, 40) || 'Неизвестный клиент'
}

export function describeBrowser(ua: string) {
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Браузер'
  const os = /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac OS/.test(ua) ? 'macOS' : /Linux/.test(ua) ? 'Linux' : ''
  return os ? `${browser}, ${os}` : browser
}

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} Б`
  const units = ['КБ', 'МБ', 'ГБ', 'ТБ']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toLocaleString('ru', { maximumFractionDigits: v < 10 ? 1 : 0 })} ${units[i]}`
}

const rtf = new Intl.RelativeTimeFormat('ru', { numeric: 'auto' })

export function relTime(iso?: string): string {
  if (!iso) return 'никогда'
  const diff = (new Date(iso).getTime() - Date.now()) / 1000
  const abs = Math.abs(diff)
  if (abs < 45) return diff < 0 ? 'только что' : 'через несколько секунд'
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day')
  return new Date(iso).toLocaleDateString('ru', { day: 'numeric', month: 'long', year: 'numeric' })
}

export function dateTime(iso?: string): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('ru', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

export function fmtInterval(min: number): string {
  if (!min) return 'вручную'
  if (min === 1440) return 'раз в сутки'
  if (min === 10080) return 'раз в неделю'
  if (min % 1440 === 0) return `каждые ${count(min / 1440, ['день', 'дня', 'дней'])}`
  if (min === 60) return 'каждый час'
  if (min % 60 === 0) return `каждые ${count(min / 60, ['час', 'часа', 'часов'])}`
  return `каждые ${min} мин`
}

export const INTERVALS: { value: number; label: string }[] = [
  { value: 0, label: 'Только вручную' },
  { value: 15, label: 'Каждые 15 минут' },
  { value: 30, label: 'Каждые 30 минут' },
  { value: 60, label: 'Каждый час' },
  { value: 180, label: 'Каждые 3 часа' },
  { value: 360, label: 'Каждые 6 часов' },
  { value: 720, label: 'Каждые 12 часов' },
  { value: 1440, label: 'Раз в сутки' },
  { value: 10080, label: 'Раз в неделю' },
]

export const BEHAVIOR_LABEL: Record<Behavior, string> = {
  auto: 'Определить автоматически',
  domain: 'domain — домены',
  ipcidr: 'ipcidr — IP-сети',
  classical: 'classical — любые правила',
}

export const INPUT_LABEL: Record<InputFormat, string> = {
  auto: 'Автоматически',
  mrs: 'MRS (бинарный mihomo)',
  yaml: 'YAML (payload:)',
  text: 'Текст — по правилу в строке',
}

export const FORMAT_INFO: Record<OutputFormat, { title: string; note: string }> = {
  mrs: { title: 'MRS', note: 'Бинарный формат mihomo: самый компактный и быстрый. Только domain и ipcidr.' },
  yaml: { title: 'YAML', note: 'payload: со списком правил — читается любым mihomo.' },
  txt: { title: 'TXT', note: 'По правилу в строке в синтаксисе mihomo: +.example.com, 1.2.3.0/24.' },
  list: { title: 'LIST', note: 'Классические строки DOMAIN-SUFFIX,example.com — behavior: classical.' },
}

const DROPPED_LABEL: Record<string, string> = { domains: 'домены', ip: 'IP-сети' }

/** "DOMAIN-KEYWORD 3, IP-сети 9" — what a behavior or an MRS file left out. */
export function droppedText(dropped?: Record<string, number>): string {
  if (!dropped) return ''
  return Object.entries(dropped)
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${DROPPED_LABEL[k] || k} ${fmtNum(n)}`)
    .join(', ')
}

/** behavior/format pair mihomo needs to read an output file. */
export function providerFields(f: OutputFormat, b: Behavior): { behavior: string; format: string } {
  if (f === 'mrs') return { behavior: b, format: 'mrs' }
  if (f === 'yaml') return { behavior: b, format: 'yaml' }
  if (f === 'list') return { behavior: 'classical', format: 'text' }
  return { behavior: b, format: 'text' }
}

export function publicBase(me: Me | null): string {
  return (me?.publicUrl || window.location.origin).replace(/\/$/, '')
}

export function rulesetLink(me: Me | null, rs: Ruleset, f: OutputFormat): string {
  const token = rs.private && rs.token ? `?token=${rs.token}` : ''
  return `${publicBase(me)}/r/${rs.slug}.${f}${token}`
}

export function fileLink(me: Me | null, bucket: string, key: string): string {
  return `${publicBase(me)}/f/${bucket}/${key.split('/').map(encodeURIComponent).join('/')}`
}

export function providerSnippet(me: Me | null, rs: Ruleset, f: OutputFormat): string {
  const b = rs.state.outputs?.[f]?.behavior || rs.state.behavior || (rs.behavior === 'auto' ? 'domain' : rs.behavior)
  const p = providerFields(f, b)
  const name = rs.slug.replace(/[^a-z0-9_-]/gi, '-')
  const interval = rs.intervalMin ? rs.intervalMin * 60 : 86400
  return [
    'rule-providers:',
    `  ${name}:`,
    '    type: http',
    `    behavior: ${p.behavior}`,
    `    format: ${p.format}`,
    `    url: ${rulesetLink(me, rs, f)}`,
    `    interval: ${interval}`,
  ].join('\n')
}

export function isImage(contentType: string) {
  return contentType.startsWith('image/')
}

export function isText(contentType: string) {
  return (
    contentType.startsWith('text/') ||
    contentType.startsWith('application/json') ||
    contentType.startsWith('application/xml')
  )
}

export function isRuleFile(key: string) {
  return /\.(mrs|ya?ml|txt|list|conf)$/i.test(key)
}
