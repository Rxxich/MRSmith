export type Behavior = 'auto' | 'domain' | 'ipcidr' | 'classical'
export type OutputFormat = 'mrs' | 'yaml' | 'txt' | 'list'
export type InputFormat = 'auto' | 'mrs' | 'yaml' | 'text'
export type Status = 'pending' | 'ok' | 'error'
export type ExcludeMode = 'exact' | 'tree' | 'parents' | 'cut'

export interface Exclusion {
  rule: string
  mode: ExcludeMode
}

export interface Me {
  user: { username: string; passwordChangedAt: string; mustChangePassword: boolean }
  csrf: string
  publicUrl: string
  panelPath: string
  version: string
  limits: { maxUpload: number; maxFetch: number }
  formats: OutputFormat[]
}

/** How often a published file was fetched. A check is a "not modified" answer. */
export interface Counter {
  downloads: number
  checks: number
  lastAt?: string
}

export interface SourceState {
  status: Status
  error?: string
  checkedAt?: string
  changedAt?: string
  sha256?: string
  size: number
  rules: number
  invalid: number
  invalidSample?: string[]
  detectedFormat?: InputFormat
  detectedBehavior?: Behavior
  cacheKey?: string
}

export interface Source {
  id: string
  type: 'url' | 'object'
  url?: string
  bucket?: string
  key?: string
  format: InputFormat
  bareAsSuffix: boolean
  state: SourceState
}

export interface OutputInfo {
  size: number
  sha256?: string
  verified: boolean
  error?: string
  /** Set when the file keeps only part of a mixed set (MRS of a classical set). */
  behavior?: Behavior
  count?: number
  dropped?: Record<string, number>
  translated?: number
}

export interface RulesetState {
  status: Status
  error?: string
  lastRunAt?: string
  lastChangeAt?: string
  nextRunAt?: string
  behavior?: Behavior
  count: number
  domains: number
  cidrs: number
  classical: number
  skipped: number
  dropped?: Record<string, number>
  optimized: number
  still?: { rule: string; by: string }[]
  outputs?: Partial<Record<OutputFormat, OutputInfo>>
  hash?: string
  durationMs: number
}

/** GET /api/rulesets (the list) sends include and exclude as null and omits state.still. */
export interface Ruleset {
  id: string
  name: string
  slug: string
  description: string
  behavior: Behavior
  formats: OutputFormat[]
  sources: Source[]
  include: string[]
  exclude: Exclusion[]
  optimize: boolean
  aggregateIP: boolean
  noResolve: boolean
  intervalMin: number
  private: boolean
  notify: boolean
  token?: string
  createdAt: string
  updatedAt: string
  state: RulesetState
  downloads?: Partial<Record<OutputFormat, Counter>>
  running: boolean
}

export interface SourceInput {
  id?: string
  type: 'url' | 'object'
  url?: string
  bucket?: string
  key?: string
  format: InputFormat
  bareAsSuffix: boolean
}

export interface RulesetInput {
  name: string
  slug: string
  description: string
  behavior: Behavior
  formats: OutputFormat[]
  sources: SourceInput[]
  include: string[]
  exclude: Exclusion[]
  optimize: boolean
  aggregateIP: boolean
  noResolve: boolean
  intervalMin: number
  private: boolean
  notify: boolean
}

export interface RulesetEvent {
  id: string
  time: string
  rulesetId: string
  rulesetName: string
  kind: 'changed' | 'unchanged' | 'error'
  trigger: string
  added: number
  removed: number
  total: number
  sampleAdded?: string[]
  sampleRemoved?: string[]
  message?: string
}

export interface Bucket {
  name: string
  public: boolean
  system: boolean
  managed: boolean
  createdAt: string
  objects: number
  bytes: number
}

export interface StorageObject {
  bucket: string
  key: string
  size: number
  contentType: string
  sha256: string
  createdAt: string
  updatedAt: string
  origin?: string
  downloads?: Counter
}

export interface Listing {
  prefix: string
  folders: string[]
  objects: StorageObject[]
  total: number
  totalBytes: number
}

export interface ConvertSummary {
  input: {
    format: InputFormat
    behavior: Behavior
    lines: number
    invalidCount: number
    invalid: string[]
    size: number
    name: string
  }
  result: {
    behavior: Behavior
    count: number
    domains: number
    cidrs: number
    classical: number
    skipped: number
    dropped?: Record<string, number>
    optimized: number
    sample: string[]
    supported: Record<OutputFormat, boolean>
    /** The part of a mixed list that goes into an MRS file. */
    mrs?: { behavior: Behavior; count: number; dropped?: Record<string, number>; translated: number; sample: string[] }
    mrsError?: string
    target?: OutputFormat
    saved?: { bucket: string; key: string }
  }
}

export interface Overview {
  rulesets: { total: number; ok: number; errors: number; rules: number; sources: number; auto: number }
  storage: { buckets: number; objects: number; bytes: number }
  events: RulesetEvent[]
  version: string
}

export interface SessionInfo {
  id: string
  current: boolean
  createdAt: string
  lastSeen: string
  expiresAt: string
  ip: string
  userAgent: string
}

export type DownloadOutcome = 'ok' | 'not-modified' | 'aborted' | 'bad-token' | 'missing' | 'private' | 'error'

export interface DownloadHit {
  time: string
  ip: string
  userAgent: string
  kind: 'ruleset' | 'file'
  rulesetId?: string
  bucket?: string
  key?: string
  name: string
  outcome: DownloadOutcome
  bytes?: number
  size?: number
}

export interface DownloadClient {
  ip: string
  userAgent: string
  downloads: number
  checks: number
  failed: number
  firstAt: string
  lastAt: string
  files: string[]
  moreFiles: number
  lastFile: string
}

/** One page of the journal of public downloads. */
export interface DownloadJournal {
  totals: { hits: number; downloads: number; checks: number; failed: number; clients: number; since?: string; keep: number }
  total: number
  recent: DownloadHit[]
  clients: DownloadClient[]
}

export interface ProxyInfo {
  url: string
  remoteDns: boolean
  auth: boolean
}

export interface Integrations {
  githubProxy: ProxyInfo | null
  telegram: { enabled: boolean; chats: number; proxy: ProxyInfo | null }
}

export interface AuditCandidate {
  id: string
  kind: 'ruleset' | 'object'
  name: string
  detail: string
  rules: number
  size: number
  system: boolean
}

export interface AuditProvider extends AuditCandidate {
  behavior?: Behavior
  error?: string
}

export interface AuditOcc {
  p: number
  rule: string
  remove?: boolean
  keep?: boolean
  inner?: boolean
  union?: boolean
}

export type AuditKind = 'same' | 'covered' | 'inner'

export interface AuditFinding {
  id: number
  kind: AuditKind
  value: string
  occ: AuditOcc[]
}

export interface AuditPair {
  from: number
  to: number
  same: number
  covered: number
  inner: number
  wide: number
}

export interface AuditReport {
  id: string
  createdAt: string
  durationMs: number
  providers: AuditProvider[]
  totals: { providers: number; rules: number; findings: number; same: number; covered: number; inner: number; removable: number }
  pairs: AuditPair[]
  truncated: boolean
}

export interface AuditFixResult {
  provider: string
  action: 'excluded' | 'rewritten' | 'optimized'
  removed: number
  error?: string
}

export interface LookupMatch {
  rule: string
  relation: 'match' | 'inside' | 'text'
}

export interface LookupHit {
  id: string
  kind: 'ruleset' | 'object'
  name: string
  detail: string
  system: boolean
  matches: LookupMatch[]
  more: number
}

export interface LookupResult {
  query: string
  mode: 'domain' | 'ip' | 'text'
  checked: number
  hits: LookupHit[]
}

export type BackupKind = 'auto' | 'manual' | 'before-restore'

/** What an archive holds, read from its manifest.json. */
export interface BackupManifest {
  app: string
  format: number
  version: string
  createdAt: string
  kind: BackupKind
  server?: string
  linkBase?: string
  rulesets: number
  names?: string[]
  buckets: number
  objects: number
  bytes: number
  skipped?: number
}

export interface BackupFile {
  name: string
  size: number
  modTime: string
  /** Made by the schedule here: removed when there are too many. */
  auto: boolean
  /** Made by another panel, e.g. brought over from the old server. */
  foreign: boolean
  manifest?: BackupManifest
  error?: string
}

export interface BackupRestoreResult {
  createdAt: string
  rulesets: number
  objects: number
  bytes: number
  oldBase?: string
  newBase?: string
  rebased: number
  safety?: string
}

export interface BackupJob {
  op: 'create' | 'restore'
  kind?: BackupKind
  name?: string
  startedAt: string
  finishedAt?: string
  error?: string
  warning?: string
  restore?: BackupRestoreResult
}

export interface BackupSettings {
  everyHours: number
  keep: number
  telegram: boolean
}

export interface BackupStatus {
  dir: string
  ready: boolean
  problem?: string
  settings: BackupSettings
  telegram: boolean
  lastAutoAt?: string
  nextAutoAt?: string
  lastError?: string
  job: BackupJob | null
  last: BackupJob | null
  files: BackupFile[]
}
