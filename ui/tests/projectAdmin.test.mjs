// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  AdminError, deleteProject, describeError, listAliases, listProjects, mergeProject, removeAlias
} from '../src/utils/projectAdmin.ts'

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

test('listProjects reads the summary uncached', async () => {
  const f = fakeFetch({ status: 200, body: [{ project: 'a_111111' }] })
  const rows = await listProjects(f)
  assert.deepEqual(rows, [{ project: 'a_111111' }])
  assert.equal(f.calls[0].url, '/api/projects/summary')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('listAliases reads the alias list uncached', async () => {
  const f = fakeFetch({ status: 200, body: [] })
  await listAliases(f)
  assert.equal(f.calls[0].url, '/api/projects/aliases')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('deleteProject without a token asks for a preview and never sends confirm', async () => {
  const f = fakeFetch({ status: 200, body: { dry_run: true, confirm: 'tok' } })
  const r = await deleteProject('repo_aaaaaa', undefined, f)
  assert.equal(r.confirm, 'tok')
  assert.deepEqual(f.calls[0], { url: '/api/projects/repo_aaaaaa', method: 'DELETE', body: undefined, cache: undefined })
})

test('deleteProject with a token sends exactly that token', async () => {
  const f = fakeFetch({ status: 200, body: { dry_run: false } })
  await deleteProject('repo_aaaaaa', 'tok 1/2', f)
  assert.equal(f.calls[0].url, '/api/projects/repo_aaaaaa?confirm=tok%201%2F2', 'the token is URL-encoded')
})

test('project ids are encoded as a single path segment', async () => {
  const f = fakeFetch({ status: 200, body: {} }, { status: 200, body: {} })
  await deleteProject('a/b c', undefined, f)
  await mergeProject('a/b c', 'x', undefined, f)
  assert.equal(f.calls[0].url, '/api/projects/a%2Fb%20c')
  assert.equal(f.calls[1].url, '/api/projects/a%2Fb%20c/merge')
})

test('mergeProject posts the target and an empty confirm for the preview', async () => {
  const f = fakeFetch({ status: 200, body: { dry_run: true } })
  await mergeProject('frag_bbbbbb', 'repo_aaaaaa', undefined, f)
  assert.equal(f.calls[0].method, 'POST')
  assert.deepEqual(JSON.parse(f.calls[0].body), { into: 'repo_aaaaaa', confirm: '' })
})

test('mergeProject with a token posts it back', async () => {
  const f = fakeFetch({ status: 200, body: {} })
  await mergeProject('frag_bbbbbb', 'repo_aaaaaa', 'mtok', f)
  assert.deepEqual(JSON.parse(f.calls[0].body), { into: 'repo_aaaaaa', confirm: 'mtok' })
})

test('removeAlias tolerates the 204 with no body', async () => {
  const f = fakeFetch({ status: 204, body: '' })
  assert.equal(await removeAlias('frag_ffffff', f), undefined)
  assert.equal(f.calls[0].method, 'DELETE')
  assert.equal(f.calls[0].url, '/api/projects/aliases/frag_ffffff')
})

test('error answers become AdminError carrying status and the worker text', async () => {
  const f = fakeFetch({ status: 409, body: 'confirmation does not match the project\'s current state; preview again' })
  await assert.rejects(deleteProject('x_111111', 'old', f), (e) => {
    assert.ok(e instanceof AdminError)
    assert.equal(e.status, 409)
    assert.match(e.message, /confirmation does not match/)
    return true
  })
})

test('an empty error body falls back to the status text', async () => {
  const f = fakeFetch({ status: 502, statusText: 'Bad Gateway', body: '' })
  await assert.rejects(listProjects(f), (e) => e.status === 502 && e.message === 'Bad Gateway')
})

test('describeError explains the cases a person can act on', () => {
  assert.match(describeError(new AdminError(409, 'confirmation does not match the project\'s current state')), /changed since the preview/)
  assert.match(describeError(new AdminError(409, 'project id is an alias: x')), /alias/)
  assert.equal(describeError(new AdminError(404, 'project not found: x')), 'That project no longer exists.')
  assert.match(describeError(new AdminError(422, 'target project "t" does not exist')), /does not exist/)
  assert.match(describeError(new AdminError(500, 'refusing to delete: could not take a backup first')), /could not take a backup/)
  assert.equal(describeError(new Error('Request timed out')), 'Request timed out')
  assert.equal(describeError('plain'), 'plain')
})
