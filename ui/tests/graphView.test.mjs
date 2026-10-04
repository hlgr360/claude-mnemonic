// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  RELATION_KINDS, countByKind, edgeWidth, fetchGraph, filterGraph, graphSummary, neighbours, nodeSize, rebuildGraph,
  relationLabel, shortLabel, typeColor
} from '../src/utils/graphView.ts'

function fakeFetch(answer) {
  const calls = []
  const fn = async (url, init = {}) => {
    calls.push({ url, method: init.method ?? 'GET', cache: init.cache })
    return { ok: answer.status >= 200 && answer.status < 300, status: answer.status, statusText: answer.statusText ?? '', json: async () => answer.body, text: async () => (typeof answer.body === 'string' ? answer.body : '') }
  }
  fn.calls = calls
  return fn
}

const node = (id, extra = {}) => ({ id, title: `Note ${id}`, type: 'discovery', project: 'p', degree: 0, created_at_epoch: id, ...extra })
const edge = (id, source, target, type, confidence) => ({ id, source, target, type, confidence })
const graph = {
  nodes: [node(1), node(2), node(3), node(4)],
  edges: [edge(1, 2, 1, 'relates_to', 0.62), edge(2, 3, 2, 'fixes', 0.85), edge(3, 4, 1, 'relates_to', 0.7)],
  total_nodes: 4, total_edges: 3, truncated: false
}
const all = { kinds: new Set(RELATION_KINDS), minConfidence: 0 }

test('fetchGraph reads one project uncached, or all', async () => {
  const f = fakeFetch({ status: 200, body: graph })
  assert.equal((await fetchGraph('my app_abc123', f)).total_nodes, 4)
  assert.equal(f.calls[0].url, '/api/graph?project=my%20app_abc123')
  assert.equal(f.calls[0].cache, 'no-store')
  const g = fakeFetch({ status: 200, body: graph })
  await fetchGraph(null, g)
  assert.equal(g.calls[0].url, '/api/graph')
})

test('an error answer carries the worker text', async () => {
  const f = fakeFetch({ status: 400, statusText: 'Bad Request', body: 'min_confidence must be a number from 0 to 1\n' })
  await assert.rejects(fetchGraph(null, f), /min_confidence must be a number/)
  const g = fakeFetch({ status: 500, statusText: 'Internal Server Error', body: '' })
  await assert.rejects(fetchGraph(null, g), /HTTP 500: Internal Server Error/)
})

test('rebuildGraph posts', async () => {
  const f = fakeFetch({ status: 200, body: { deleted: 12, message: 'ok' } })
  assert.equal((await rebuildGraph(f)).deleted, 12)
  assert.equal(f.calls[0].url, '/api/relations/rebuild')
  assert.equal(f.calls[0].method, 'POST')
})

test('with no filter every relation and every connected note is shown, with the degrees', () => {
  const r = filterGraph(graph, all)
  assert.equal(r.edges.length, 3)
  assert.deepEqual(r.nodes.map(n => [n.id, n.degree]), [[1, 2], [2, 2], [3, 1], [4, 1]])
})

test('a confidence floor drops weak relations and the notes only they connected', () => {
  const r = filterGraph(graph, { kinds: new Set(RELATION_KINDS), minConfidence: 0.8 })
  assert.deepEqual(r.edges.map(e => e.id), [2])
  assert.deepEqual(r.nodes.map(n => n.id), [2, 3])
  assert.ok(r.nodes.every(n => n.degree === 1))
})

test('a kind filter keeps only those relations', () => {
  const r = filterGraph(graph, { kinds: new Set(['fixes']), minConfidence: 0 })
  assert.deepEqual(r.edges.map(e => e.type), ['fixes'])
  assert.deepEqual(filterGraph(graph, { kinds: new Set(), minConfidence: 0 }), { nodes: [], edges: [] })
})

test('filtering does not change the original graph', () => {
  const before = JSON.stringify(graph)
  filterGraph(graph, { kinds: new Set(['fixes']), minConfidence: 0.9 })
  assert.equal(JSON.stringify(graph), before)
})

test('neighbours lists the related notes, most certain first, and says who points at whom', () => {
  const r = neighbours(graph.nodes, graph.edges, 1)
  assert.deepEqual(r.map(n => [n.node.id, n.outgoing]), [[4, false], [2, false]], 'both point at the older note 1')
  const s = neighbours(graph.nodes, graph.edges, 2)
  assert.deepEqual(s.map(n => [n.node.id, n.outgoing]), [[3, false], [1, true]])
  assert.deepEqual(neighbours(graph.nodes, graph.edges, 99), [])
})

test('the legend counts relations by kind', () => {
  assert.deepEqual(countByKind(graph.edges), { relates_to: 2, fixes: 1 })
  assert.deepEqual(countByKind([]), {})
})

test('the summary says how much is shown, and when the answer was cut', () => {
  assert.equal(graphSummary(graph.nodes, graph.edges, graph), '4 notes, 3 relations')
  assert.equal(graphSummary([node(1)], [edge(1, 2, 1, 'fixes', 1)], graph), '1 note, 1 relation')
  assert.equal(graphSummary(graph.nodes, graph.edges, { ...graph, truncated: true, total_nodes: 900 }), '4 notes, 3 relations. Showing the best connected of 900 notes.')
})

test('sizes and widths stay in range', () => {
  assert.ok(nodeSize(0) >= 8)
  assert.ok(nodeSize(1) < nodeSize(9))
  assert.equal(nodeSize(100000), 30)
  assert.equal(nodeSize(-5), 8)
  assert.equal(edgeWidth(0), 1)
  assert.equal(edgeWidth(1), 4)
  assert.equal(edgeWidth(7), 4)
  assert.equal(edgeWidth(-1), 1)
})

test('colours and labels have fallbacks', () => {
  assert.equal(typeColor('bugfix'), '#ef4444')
  assert.equal(typeColor('nonsense'), typeColor('change'))
  assert.equal(relationLabel('evolves_from'), 'evolves from')
  assert.equal(shortLabel('  '), 'Untitled')
  assert.equal(shortLabel('a'.repeat(40), 10), 'aaaaaaaaa…')
  assert.equal(shortLabel('short'), 'short')
})
