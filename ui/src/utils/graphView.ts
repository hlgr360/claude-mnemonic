// The knowledge graph view's data and the logic around it. Free of Vue and of path aliases so it can be
// exercised directly with `node --test` (see tests/).

type FetchFn = typeof fetch

export type RelationKind = 'causes' | 'fixes' | 'supersedes' | 'depends_on' | 'relates_to' | 'evolves_from'

export interface GraphNodeData {
  id: number
  title: string
  type: string
  project: string
  concepts?: string[]
  /** Relations of this note within the graph that was returned. */
  degree: number
  created_at_epoch: number
}

export interface GraphEdgeData {
  id: number
  source: number
  target: number
  type: RelationKind
  confidence: number
  reason?: string
}

export interface GraphData {
  nodes: GraphNodeData[]
  edges: GraphEdgeData[]
  total_nodes: number
  total_edges: number
  /** The answer was cut to the best connected notes. */
  truncated: boolean
}

const defaultFetch: FetchFn = (...args) => globalThis.fetch(...args)

async function request<T>(fetchFn: FetchFn, url: string, init?: RequestInit): Promise<T> {
  const response = await fetchFn(url, init)
  if (!response.ok) {
    const text = (await response.text()).trim()
    throw new Error(text || `HTTP ${response.status}: ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

/** Reads the graph of one project, or of all of them when project is empty. */
export function fetchGraph(project?: string | null, fetchFn: FetchFn = defaultFetch): Promise<GraphData> {
  const query = project ? `?project=${encodeURIComponent(project)}` : ''
  return request(fetchFn, `/api/graph${query}`, { cache: 'no-store' })
}

/** Deletes every relation and has the worker build them again. */
export function rebuildGraph(fetchFn: FetchFn = defaultFetch): Promise<{ deleted: number; message: string }> {
  return request(fetchFn, '/api/relations/rebuild', { method: 'POST' })
}

export const RELATION_KINDS: RelationKind[] = ['relates_to', 'fixes', 'depends_on', 'evolves_from', 'causes', 'supersedes']

export const RELATION_COLORS: Record<RelationKind, string> = {
  causes: '#f97316',
  fixes: '#22c55e',
  supersedes: '#a855f7',
  depends_on: '#3b82f6',
  relates_to: '#64748b',
  evolves_from: '#06b6d4'
}

export function relationLabel(kind: string): string {
  return kind.replace(/_/g, ' ')
}

export const TYPE_COLORS: Record<string, string> = {
  bugfix: '#ef4444',
  feature: '#a855f7',
  refactor: '#3b82f6',
  discovery: '#06b6d4',
  decision: '#eab308',
  change: '#64748b'
}

export function typeColor(type: string): string {
  return TYPE_COLORS[type] ?? TYPE_COLORS.change
}

/** The size of a note's circle: more relations, bigger, but never huge. */
export function nodeSize(degree: number): number {
  return Math.min(8 + Math.sqrt(Math.max(0, degree)) * 4, 30)
}

/** A more certain relation is drawn thicker. Confidences are 0 to 1. */
export function edgeWidth(confidence: number): number {
  return 1 + Math.max(0, Math.min(1, confidence)) * 3
}

export interface GraphFilters {
  /** Relation kinds to show. */
  kinds: Set<string>
  /** Only relations at or above this confidence. */
  minConfidence: number
}

/**
 * The part of the graph the filters let through: the relations of the chosen kinds at or above the confidence,
 * and the notes that still have one, with their degree recounted.
 */
export function filterGraph(graph: GraphData, filters: GraphFilters): { nodes: GraphNodeData[]; edges: GraphEdgeData[] } {
  const edges = graph.edges.filter(e => filters.kinds.has(e.type) && e.confidence >= filters.minConfidence)
  const degree = new Map<number, number>()
  for (const e of edges) {
    degree.set(e.source, (degree.get(e.source) ?? 0) + 1)
    degree.set(e.target, (degree.get(e.target) ?? 0) + 1)
  }
  const nodes = graph.nodes.filter(n => degree.has(n.id)).map(n => ({ ...n, degree: degree.get(n.id) ?? 0 }))
  return { nodes, edges }
}

export interface Neighbour {
  node: GraphNodeData
  edge: GraphEdgeData
  /** The selected note is the newer one of the relation (it points to the other). */
  outgoing: boolean
}

/** The notes related to one note, the most certain relation first. */
export function neighbours(nodes: GraphNodeData[], edges: GraphEdgeData[], id: number): Neighbour[] {
  const byId = new Map(nodes.map(n => [n.id, n]))
  const out: Neighbour[] = []
  for (const edge of edges) {
    if (edge.source !== id && edge.target !== id) continue
    const outgoing = edge.source === id
    const node = byId.get(outgoing ? edge.target : edge.source)
    if (node) out.push({ node, edge, outgoing })
  }
  return out.sort((a, b) => b.edge.confidence - a.edge.confidence || a.node.id - b.node.id)
}

/** How many of each kind, for the legend. */
export function countByKind(edges: GraphEdgeData[]): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const e of edges) counts[e.type] = (counts[e.type] ?? 0) + 1
  return counts
}

/** One line about what is on screen, for example "52 notes, 97 relations". */
export function graphSummary(nodes: GraphNodeData[], edges: GraphEdgeData[], graph: GraphData): string {
  const base = `${nodes.length} note${nodes.length === 1 ? '' : 's'}, ${edges.length} relation${edges.length === 1 ? '' : 's'}`
  return graph.truncated ? `${base}. Showing the best connected of ${graph.total_nodes} notes.` : base
}

/** A label that fits in a circle's neighbourhood. */
export function shortLabel(title: string, max = 34): string {
  const t = (title || '').trim() || 'Untitled'
  return t.length > max ? `${t.slice(0, max - 1)}…` : t
}
