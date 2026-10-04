// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  applyScope, describePlan, fetchScopePreview, filterByScope, otherScope, scopeHint, scopeLabel, setObservationScope
} from '../src/utils/scope.ts'

function fakeFetch(answer) {
  const calls = []
  const fn = async (url, init = {}) => {
    calls.push({ url, method: init.method ?? 'GET', body: init.body, cache: init.cache })
    return { ok: answer.status >= 200 && answer.status < 300, status: answer.status, statusText: answer.statusText ?? '', json: async () => answer.body, text: async () => (typeof answer.body === 'string' ? answer.body : '') }
  }
  fn.calls = calls
  return fn
}

test('the preview is read uncached', async () => {
  const f = fakeFetch({ status: 200, body: { total: 3, to_project: 2, sample: [] } })
  assert.equal((await fetchScopePreview(f)).total, 3)
  assert.equal(f.calls[0].url, '/api/scope/preview')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('apply sends the token of the preview', async () => {
  const f = fakeFetch({ status: 200, body: { changed: 2 } })
  await applyScope('abc123', f)
  assert.equal(f.calls[0].url, '/api/scope/apply')
  assert.equal(f.calls[0].method, 'POST')
  assert.deepEqual(JSON.parse(f.calls[0].body), { confirm: 'abc123' })
})

test('a stale token is an error that carries its status and the worker text', async () => {
  const f = fakeFetch({ status: 409, statusText: 'Conflict', body: 'confirmation does not match the archive\'s current state; preview again\n' })
  await assert.rejects(applyScope('old', f), err => err.status === 409 && /preview again/.test(err.message))
})

test('setting one note\'s scope edits only the scope', async () => {
  const f = fakeFetch({ status: 200, body: {} })
  await setObservationScope(42, 'global', f)
  assert.equal(f.calls[0].url, '/api/observations/42')
  assert.equal(f.calls[0].method, 'PUT')
  assert.deepEqual(JSON.parse(f.calls[0].body), { scope: 'global' })
})

test('scope names, hints and the other scope', () => {
  assert.equal(otherScope('global'), 'project')
  assert.equal(otherScope('project'), 'global')
  assert.equal(scopeLabel('global'), 'Global')
  assert.equal(scopeLabel('project'), 'Project')
  assert.equal(scopeLabel(undefined), 'Project', 'a note without a scope is a project note')
  assert.match(scopeHint('global'), /every project/)
  assert.match(scopeHint('project'), /own project/)
})

test('the scope filter keeps observations of that scope and leaves out what has none', () => {
  const items = [
    { id: 1, itemType: 'observation', scope: 'global' },
    { id: 2, itemType: 'observation', scope: 'project' },
    { id: 3, itemType: 'observation' },
    { id: 4, itemType: 'prompt' },
    { id: 5, itemType: 'summary' }
  ]
  assert.deepEqual(filterByScope(items, 'all').map(i => i.id), [1, 2, 3, 4, 5])
  assert.deepEqual(filterByScope(items, 'global').map(i => i.id), [1])
  assert.deepEqual(filterByScope(items, 'project').map(i => i.id), [2, 3])
})

test('the plan is described in a sentence', () => {
  assert.equal(describePlan({ total: 416, to_project: 340, to_global: 0, protected: 2 }), '340 of 416 notes would change scope (340 to project). 2 keep a scope that was chosen by hand.')
  assert.equal(describePlan({ total: 10, to_project: 3, to_global: 1, protected: 0 }), '4 of 10 notes would change scope (3 to project, 1 to global).')
  assert.match(describePlan({ total: 12, to_project: 0, to_global: 0, protected: 1 }), /Nothing to change/)
})
