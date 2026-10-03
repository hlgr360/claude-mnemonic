// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildRows, diffList, diffWords, formatSaved, tokenize } from '../src/utils/conflictDiff.ts'

const text = segments => segments.map(s => s.text).join('')
const changed = segments => segments.filter(s => s.changed).map(s => s.text)

test('tokenize keeps the whitespace, so joining gives the text back', () => {
  const s = 'The cache  lives\tfor an hour'
  assert.equal(tokenize(s).join(''), s)
  assert.deepEqual(tokenize(''), [])
})

test('identical texts have nothing changed', () => {
  const d = diffWords('same words here', '  same words here ')
  assert.equal(d.same, true)
  assert.deepEqual(d.left, [{ text: 'same words here', changed: false }])
  assert.deepEqual(d.right, [{ text: 'same words here', changed: false }])
})

test('only the words that differ are marked', () => {
  const d = diffWords('The rate cache lives for 60 minutes', 'The rate cache lives for 24 hours')
  assert.equal(d.same, false)
  assert.equal(text(d.left), 'The rate cache lives for 60 minutes')
  assert.equal(text(d.right), 'The rate cache lives for 24 hours')
  assert.deepEqual(changed(d.left), ['60 minutes'])
  assert.deepEqual(changed(d.right), ['24 hours'])
})

test('words found in both stay unchanged even when moved around the changes', () => {
  const d = diffWords('alpha beta gamma delta', 'alpha gamma delta epsilon')
  assert.deepEqual(changed(d.left), ['beta'])
  assert.deepEqual(changed(d.right), ['epsilon'])
})

test('a changed run is one segment, including the space between its words', () => {
  const d = diffWords('keep one two three keep', 'keep four five keep')
  assert.deepEqual(d.left.map(s => [s.text, s.changed]), [['keep ', false], ['one two three', true], [' keep', false]])
  assert.deepEqual(d.right.map(s => [s.text, s.changed]), [['keep ', false], ['four five', true], [' keep', false]])
})

test('different amounts of whitespace are not a difference', () => {
  const d = diffWords('a  b\nc', 'a b c')
  assert.equal(d.left.some(s => s.changed), false)
  assert.equal(d.right.some(s => s.changed), false)
})

test('one side empty means the other is entirely changed', () => {
  const d = diffWords('', 'brand new text')
  assert.deepEqual(d.left, [])
  assert.deepEqual(d.right, [{ text: 'brand new text', changed: true }])
  const e = diffWords('gone', '')
  assert.deepEqual(e.left, [{ text: 'gone', changed: true }])
  assert.deepEqual(e.right, [])
  assert.deepEqual(diffWords('', '').left, [])
})

test('very long texts fall back to everything changed instead of hanging', () => {
  const a = Array.from({ length: 800 }, (_, i) => `a${i}`).join(' ')
  const b = Array.from({ length: 800 }, (_, i) => `b${i}`).join(' ')
  const d = diffWords(a, b)
  assert.deepEqual(d.left, [{ text: a, changed: true }])
  assert.deepEqual(d.right, [{ text: b, changed: true }])
})

test('lists mark the items the other side lacks, ignoring case and padding', () => {
  const d = diffList(['Redis TTL', 'only old', ' shared '], ['redis ttl', 'shared', 'only new'])
  assert.deepEqual(d.left.map(x => [x.text, x.changed]), [['Redis TTL', false], ['only old', true], ['shared', false]])
  assert.deepEqual(d.right.map(x => [x.text, x.changed]), [['redis ttl', false], ['shared', false], ['only new', true]])
  assert.equal(d.same, false)
  assert.equal(diffList(['a'], ['A ']).same, true)
  assert.deepEqual(diffList(['', '  '], []).left, [])
})

test('formatSaved gives the UTC day for seconds and for milliseconds', () => {
  const ms = Date.UTC(2026, 9, 3, 23, 59)
  assert.equal(formatSaved(ms), '2026-10-03')
  assert.equal(formatSaved(Math.floor(ms / 1000)), '2026-10-03')
  assert.equal(formatSaved(0), '')
})

const obs = (extra = {}) => ({
  id: 1, title: '', subtitle: '', narrative: '', facts: [], concepts: [], files_read: [], files_modified: [],
  type: 'discovery', created_at_epoch: Date.UTC(2026, 9, 1), ...extra
})

test('rows cover the fields that are filled in and skip the empty ones', () => {
  const rows = buildRows(
    obs({ title: 'Cache for an hour', narrative: 'Lives 60 minutes', facts: ['ttl 60m'], concepts: ['cache'] }),
    obs({ title: 'Cache for a day', narrative: 'Lives 24 hours', facts: ['ttl 24h'], concepts: ['cache'], created_at_epoch: Date.UTC(2026, 9, 3) })
  )
  assert.deepEqual(rows.map(r => r.key), ['title', 'narrative', 'facts', 'concepts', 'type', 'saved'])
  const byKey = Object.fromEntries(rows.map(r => [r.key, r]))
  assert.equal(byKey.title.same, false)
  assert.equal(byKey.concepts.same, true)
  assert.equal(byKey.type.same, true)
  assert.equal(byKey.saved.same, false)
  assert.deepEqual(byKey.saved.left, [{ text: '2026-10-01', changed: false }])
  assert.deepEqual(byKey.saved.right, [{ text: '2026-10-03', changed: false }])
  assert.deepEqual(byKey.facts.left, [{ text: 'ttl 60m', changed: true }])
})

test('files of both kinds are listed once', () => {
  const rows = buildRows(
    obs({ files_read: ['a.go'], files_modified: ['a.go', 'b.go'] }),
    obs({ files_read: ['b.go'], files_modified: ['c.go'] })
  )
  const files = rows.find(r => r.key === 'files')
  assert.deepEqual(files.left.map(x => [x.text, x.changed]), [['a.go', true], ['b.go', false]])
  assert.deepEqual(files.right.map(x => [x.text, x.changed]), [['c.go', true], ['b.go', false]])
})

test('missing arrays and fields do not break the rows', () => {
  const rows = buildRows({ id: 1, created_at_epoch: 0 }, { id: 2, title: 'Only here', facts: null, created_at_epoch: 0 })
  assert.deepEqual(rows.map(r => r.key), ['title'])
  assert.deepEqual(rows[0].left, [])
  assert.deepEqual(rows[0].right, [{ text: 'Only here', changed: true }])
})
