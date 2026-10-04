// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { count, normalizeAnalytics, normalizeRecent, percent, timeAgo } from '../src/utils/searchAnalytics.ts'

// What the worker sends today (handleGetSearchAnalytics)
const FULL = {
  total_queries: 10, vector_searches: 6, keyword_searches: 4, vector_search_rate: 60, avg_results: 2.5, zero_result_rate: 20,
  query_types: { observations: 8, summaries: 2 }, top_keywords: [{ keyword: 'retry', count: 5 }, { keyword: 'queue', count: 2 }], project: 'alpha_aaaaaa'
}

test('the worker answer is read as it is', () => {
  assert.deepEqual(normalizeAnalytics(FULL), FULL)
})

test('the answer that crashed the popup (no total_searches) no longer can: every field is a number or empty', () => {
  const a = normalizeAnalytics({})
  assert.equal(a.total_queries, 0)
  assert.equal(count(a.total_queries), '0', 'what the popup calls on it')
  assert.equal(a.vector_searches, 0)
  assert.equal(a.avg_results.toFixed(1), '0.0')
  assert.deepEqual(a.query_types, {})
  assert.deepEqual(a.top_keywords, [])
  for (const bad of [null, undefined, 'x', 3, [], [1]]) {
    assert.equal(normalizeAnalytics(bad).total_queries, 0, String(bad))
  }
})

test('malformed fields become 0 or are dropped, the rest survives', () => {
  const a = normalizeAnalytics({ total_queries: '10', vector_search_rate: NaN, avg_results: null, query_types: { a: 'x', b: 3 }, top_keywords: [{ keyword: 'ok', count: 2 }, { keyword: '', count: 9 }, 5, null], project: 7 })
  assert.equal(a.total_queries, 0)
  assert.equal(a.vector_search_rate, 0)
  assert.equal(a.avg_results, 0)
  assert.deepEqual(a.query_types, { a: 0, b: 3 })
  assert.deepEqual(a.top_keywords, [{ keyword: 'ok', count: 2 }])
  assert.equal(a.project, '')
})

test('an older worker that sent only the rate gets its counts derived', () => {
  const a = normalizeAnalytics({ total_queries: 10, vector_search_rate: 30 })
  assert.equal(a.vector_searches, 3)
  assert.equal(a.keyword_searches, 7)
})

test('recent searches: {queries: [...]} or a bare list, malformed entries dropped', () => {
  const q = { query: 'retry policy', project: 'alpha_aaaaaa', type: 'observations', results: 3, used_vector: true, timestamp: '2026-10-04T10:00:00Z' }
  const expected = [{ ...q }]
  assert.deepEqual(normalizeRecent({ queries: [q], count: 1, project: '' }), expected)
  assert.deepEqual(normalizeRecent([q]), expected)
  assert.deepEqual(normalizeRecent({ queries: [q, null, 4, { query: '' }, { results: 2 }] }), expected)
  assert.deepEqual(normalizeRecent({ queries: [{ query: 'x' }] }), [{ query: 'x', project: undefined, type: undefined, results: 0, used_vector: false, timestamp: '' }])
  for (const bad of [null, undefined, {}, 'x', { queries: 'x' }]) assert.deepEqual(normalizeRecent(bad), [])
})

test('timeAgo reads an ISO time and never says NaN', () => {
  const now = Date.parse('2026-10-04T12:00:00Z')
  assert.equal(timeAgo('2026-10-04T11:59:40Z', now), 'just now')
  assert.equal(timeAgo('2026-10-04T11:55:00Z', now), '5m ago')
  assert.equal(timeAgo('2026-10-04T09:00:00Z', now), '3h ago')
  assert.equal(timeAgo('2026-10-02T12:00:00Z', now), '2d ago')
  assert.equal(timeAgo('2026-10-05T12:00:00Z', now), 'just now', 'a clock a little ahead is not negative')
  assert.equal(timeAgo(undefined, now), '')
  assert.equal(timeAgo('', now), '')
  assert.equal(timeAgo('not a date', now), '')
})

test('count and percent format any number, including a bad one', () => {
  assert.equal(count(1234567), '1,234,567')
  assert.equal(count(NaN), '0')
  assert.equal(percent(12.345), '12.3%')
  assert.equal(percent(150), '100.0%')
  assert.equal(percent(-3), '0.0%')
  assert.equal(percent(NaN), '0.0%')
})
