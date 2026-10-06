// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { DEFAULT_FILTER, FILTER_TABS } from '../src/utils/tabs.ts'

test('the dashboard opens on the Summaries tab', () => {
  assert.equal(DEFAULT_FILTER, 'summaries')
})

test('the default is one of the tabs, and All is still a tab', () => {
  const keys = FILTER_TABS.map(t => t.key)
  assert.ok(keys.includes(DEFAULT_FILTER))
  assert.ok(keys.includes('all'))
})

test('the tabs are the ones the dashboard has, in order, each with a label and an icon', () => {
  assert.deepEqual(FILTER_TABS.map(t => t.key), ['all', 'observations', 'summaries', 'prompts', 'graph', 'conflicts', 'folds'])
  for (const t of FILTER_TABS) {
    assert.ok(t.label.length > 0 && t.icon.startsWith('fa-'), t.key)
  }
})
