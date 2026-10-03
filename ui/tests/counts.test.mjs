// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { fetchTotals, showingText } from '../src/utils/counts.ts'

function fakeFetch(answer) {
  const calls = []
  const fn = async (url, init = {}) => {
    calls.push({ url, cache: init.cache })
    return { ok: answer.status >= 200 && answer.status < 300, status: answer.status, statusText: answer.statusText ?? '', json: async () => answer.body }
  }
  fn.calls = calls
  return fn
}

test('fetchTotals reads all projects uncached', async () => {
  const f = fakeFetch({ status: 200, body: { observations: 415, prompts: 372, summaries: 151 } })
  assert.deepEqual(await fetchTotals(null, f), { observations: 415, prompts: 372, summaries: 151 })
  assert.equal(f.calls[0].url, '/api/counts')
  assert.equal(f.calls[0].cache, 'no-store')
})

test('fetchTotals asks for one project and encodes it', async () => {
  const f = fakeFetch({ status: 200, body: { observations: 1, prompts: 0, summaries: 0 } })
  await fetchTotals('my app_abc123', f)
  assert.equal(f.calls[0].url, '/api/counts?project=my%20app_abc123')
})

test('fetchTotals turns an error answer into an error', async () => {
  const f = fakeFetch({ status: 500, statusText: 'Internal Server Error', body: {} })
  await assert.rejects(fetchTotals(null, f), /HTTP 500: Internal Server Error/)
})

const totals = { observations: 415, prompts: 372, summaries: 151 }

test('a list that is complete says nothing', () => {
  assert.equal(showingText({ observations: 415, prompts: 372, summaries: 151 }, totals), '')
  assert.equal(showingText({ observations: 3, prompts: 0, summaries: 0 }, { observations: 3, prompts: 0, summaries: 0 }), '')
})

test('the totals are not known yet', () => {
  assert.equal(showingText({ observations: 50, prompts: 50, summaries: 50 }, null), '')
})

test('every truncated list is named with its total', () => {
  assert.equal(
    showingText({ observations: 50, prompts: 50, summaries: 50 }, totals),
    'Showing the newest 50 of 415 observations, 50 of 372 prompts and 50 of 151 summaries.'
  )
})

test('only the lists that are cut are mentioned', () => {
  assert.equal(showingText({ observations: 50, prompts: 7, summaries: 3 }, { observations: 415, prompts: 7, summaries: 3 }),
    'Showing the newest 50 of 415 observations.')
  assert.equal(showingText({ observations: 50, prompts: 50, summaries: 3 }, { observations: 415, prompts: 372, summaries: 3 }),
    'Showing the newest 50 of 415 observations and 50 of 372 prompts.')
})
