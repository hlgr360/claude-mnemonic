// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { observationsQuery } from '../src/utils/observationsQuery.ts'

const parse = (q) => Object.fromEntries(new URLSearchParams(q))

test('the timeline asks for the newest observations by default', () => {
  assert.deepEqual(parse(observationsQuery(50)), { limit: '50', sort: 'date' })
})

test('a project is added, encoded, and the sort is kept', () => {
  assert.deepEqual(parse(observationsQuery(100, 'pantry app_1a2b3c')), { limit: '100', project: 'pantry app_1a2b3c', sort: 'date' })
  assert.match(observationsQuery(100, 'a&b=c'), /project=a%26b%3Dc/, 'the project cannot add parameters of its own')
})

test('the most important first can still be asked for', () => {
  assert.equal(parse(observationsQuery(20, undefined, 'importance')).sort, 'importance')
})

test('an empty project means all projects', () => {
  assert.equal('project' in parse(observationsQuery(50, '')), false)
  assert.equal('project' in parse(observationsQuery(50, undefined)), false)
})

test('the request the timeline makes for all projects and for one project', async () => {
  // useTimeline fetches 50 observations for all projects and 100 for one; both must be newest first.
  for (const [limit, project] of [[50, undefined], [100, 'p_123456']]) {
    assert.equal(parse(observationsQuery(limit, project)).sort, 'date', `limit ${limit}`)
  }
})
