// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  ConflictApiError, conflictQuery, countOpenConflicts, describeConflictError, listConflicts, proposeConflict,
  resolveConflict, undoConflict
} from '../src/utils/conflicts.ts'

/** A fetch that records calls and answers from a queue. */
function fakeFetch(...answers) {
  const calls = []
  const fn = async (url, init = {}) => {
    calls.push({ url, method: init.method ?? 'GET', body: init.body, cache: init.cache })
    const a = answers.shift() ?? { status: 200, body: {} }
    return {
      ok: a.status >= 200 && a.status < 300,
      status: a.status,
      statusText: a.statusText ?? '',
      json: async () => a.body,
      text: async () => (typeof a.body === 'string' ? a.body : JSON.stringify(a.body))
    }
  }
  fn.calls = calls
  return fn
}

test('the query leaves out empty values', () => {
  assert.equal(conflictQuery(), '')
  assert.equal(conflictQuery({ status: 'open', project: null, limit: 0 }), '?status=open')
  assert.equal(conflictQuery({ status: 'all', project: 'a b_111111', limit: 20, offset: 40 }), '?status=all&project=a+b_111111&limit=20&offset=40')
})

test('listConflicts reads the list uncached', async () => {
  const f = fakeFetch({ status: 200, body: { conflicts: [], total: 0, open_count: 0 } })
  const out = await listConflicts({ status: 'resolved', project: 'p_111111' }, f)
  assert.deepEqual(out, { conflicts: [], total: 0, open_count: 0 })
  assert.equal(f.calls[0].url, '/api/conflicts?status=resolved&project=p_111111')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('countOpenConflicts returns the number', async () => {
  const f = fakeFetch({ status: 200, body: { open: 7 } })
  assert.equal(await countOpenConflicts('p_111111', f), 7)
  assert.equal(f.calls[0].url, '/api/conflicts/count?project=p_111111')
  const g = fakeFetch({ status: 200, body: { open: 0 } })
  await countOpenConflicts(null, g)
  assert.equal(g.calls[0].url, '/api/conflicts/count')
})

test('resolveConflict posts the decision', async () => {
  const f = fakeFetch({ status: 200, body: { id: 5, resolved: true } })
  const c = await resolveConflict(5, 'keep_both', f)
  assert.equal(c.id, 5)
  assert.equal(f.calls[0].url, '/api/conflicts/5/resolve')
  assert.equal(f.calls[0].method, 'POST')
  assert.deepEqual(JSON.parse(f.calls[0].body), { decision: 'keep_both' })
})

test('undoConflict posts without a body', async () => {
  const f = fakeFetch({ status: 200, body: { id: 5, resolved: false } })
  await undoConflict(5, f)
  assert.equal(f.calls[0].url, '/api/conflicts/5/undo')
  assert.equal(f.calls[0].method, 'POST')
  assert.equal(f.calls[0].body, undefined)
})

test('proposeConflict sends the pair and the reason', async () => {
  const f = fakeFetch({ status: 201, body: { id: 9 } })
  await proposeConflict(1, 2, 'same setting', f)
  assert.equal(f.calls[0].url, '/api/conflicts')
  assert.deepEqual(JSON.parse(f.calls[0].body), { older_id: 1, newer_id: 2, reason: 'same setting' })
})

test('an error answer becomes a ConflictApiError with its status and text', async () => {
  const f = fakeFetch({ status: 409, body: 'conflict is already resolved\n' })
  await assert.rejects(resolveConflict(5, 'keep_both', f), err => {
    assert.ok(err instanceof ConflictApiError)
    assert.equal(err.status, 409)
    assert.equal(err.message, 'conflict is already resolved')
    return true
  })
})

test('an empty error body falls back to the status text', async () => {
  const f = fakeFetch({ status: 500, statusText: 'Internal Server Error', body: '' })
  await assert.rejects(listConflicts({}, f), err => err.message === 'Internal Server Error')
})

test('errors are described for a person', () => {
  assert.match(describeConflictError(new ConflictApiError(404, 'x')), /no longer exists/)
  assert.match(describeConflictError(new ConflictApiError(409, 'x')), /already decided/)
  assert.match(describeConflictError(new ConflictApiError(410, 'x')), /deleted/)
  assert.equal(describeConflictError(new ConflictApiError(422, 'not a valid pair')), 'not a valid pair')
  assert.match(describeConflictError(new ConflictApiError(500, 'boom')), /could not complete.*boom/)
  assert.equal(describeConflictError(new ConflictApiError(418, 'teapot')), 'teapot')
  assert.equal(describeConflictError(new Error('offline')), 'offline')
  assert.equal(describeConflictError('plain'), 'plain')
})
