// What the Search Analytics popup shows, and the tolerant reading of what the worker sends. Free of Vue and of path
// aliases so it can be exercised directly with `node --test` (see tests/).
//
// The worker (GET /api/search/analytics, GET /api/search/recent) counts the last 100 searches it served since it
// started. It does not measure latency or cache hits. A missing or malformed field becomes 0 or empty: one absent
// number never blanks the popup.

export interface SearchAnalytics {
  total_queries: number
  vector_searches: number
  keyword_searches: number
  /** Percent of the searches that used the vector index. */
  vector_search_rate: number
  avg_results: number
  /** Percent of the searches that found nothing. */
  zero_result_rate: number
  query_types: Record<string, number>
  top_keywords: { keyword: string; count: number }[]
  project: string
}

export interface RecentQuery {
  query: string
  project?: string
  type?: string
  results: number
  used_vector: boolean
  timestamp: string
}

const num = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0)
const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

/** Reads the analytics answer; anything missing is 0, an empty map or an empty list. */
export function normalizeAnalytics(raw: unknown): SearchAnalytics {
  const r = isObject(raw) ? raw : {}
  const total = Math.max(0, Math.round(num(r.total_queries)))
  const rate = num(r.vector_search_rate)
  // Older workers sent only the rate; derive the counts from it.
  const vector = 'vector_searches' in r ? Math.max(0, Math.round(num(r.vector_searches))) : Math.round((total * rate) / 100)
  const keyword = 'keyword_searches' in r ? Math.max(0, Math.round(num(r.keyword_searches))) : Math.max(0, total - vector)

  const types: Record<string, number> = {}
  if (isObject(r.query_types)) {
    for (const [k, v] of Object.entries(r.query_types)) types[k] = num(v)
  }
  const keywords = Array.isArray(r.top_keywords)
    ? r.top_keywords.filter(isObject).map(k => ({ keyword: str(k.keyword), count: num(k.count) })).filter(k => k.keyword !== '')
    : []

  return {
    total_queries: total,
    vector_searches: vector,
    keyword_searches: keyword,
    vector_search_rate: rate,
    avg_results: num(r.avg_results),
    zero_result_rate: num(r.zero_result_rate),
    query_types: types,
    top_keywords: keywords,
    project: str(r.project)
  }
}

/** Reads the recent searches: the worker sends {queries: [...]}; a bare list is accepted too. */
export function normalizeRecent(raw: unknown): RecentQuery[] {
  const list = Array.isArray(raw) ? raw : isObject(raw) && Array.isArray(raw.queries) ? raw.queries : []
  return list
    .filter(isObject)
    .map(q => ({
      query: str(q.query),
      project: str(q.project) || undefined,
      type: str(q.type) || undefined,
      results: num(q.results),
      used_vector: q.used_vector === true,
      timestamp: str(q.timestamp)
    }))
    .filter(q => q.query !== '')
}

/** "just now", "5m ago", "3h ago", "2d ago"; empty when the time cannot be read. */
export function timeAgo(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const at = new Date(iso).getTime()
  if (!Number.isFinite(at)) return ''
  const diff = Math.max(0, now - at)
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(diff / 3600000)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(diff / 86400000)}d ago`
}

/** 1 234 style thousands, for any number. */
export function count(n: number): string {
  return (Number.isFinite(n) ? n : 0).toLocaleString('en-US')
}

/** A percentage with one decimal, clamped to 0 to 100. */
export function percent(n: number): string {
  const v = Number.isFinite(n) ? Math.min(100, Math.max(0, n)) : 0
  return `${v.toFixed(1)}%`
}
