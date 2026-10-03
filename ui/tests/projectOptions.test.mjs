// Run with: npm test   (node --test, TypeScript is stripped by Node itself)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildProjectOptions, optionMatches, selectedText } from '../src/utils/projectOptions.ts'

const row = (project, display_name, extra = {}) => ({
  project, display_name, label: display_name, sessions: 1, observations: 3, summaries: 0, last_active_epoch: 1, ...extra
})

test('a unique name is shown without its hash', () => {
  const [o] = buildProjectOptions([row('claude-mnemonic_41bfcd', 'claude-mnemonic')])
  assert.equal(o.text, 'claude-mnemonic')
  assert.equal(o.name, 'claude-mnemonic')
  assert.equal(o.id, 'claude-mnemonic_41bfcd')
  assert.equal(o.namesake, false)
})

test('projects that share a name show what tells them apart, and the id is kept for display', () => {
  const opts = buildProjectOptions([
    row('app_111111', 'app', { label: 'app (2 observations, last used 2026-10-01)' }),
    row('app_222222', 'app', { label: 'app (5 observations, last used 2026-10-03)' })
  ])
  assert.deepEqual(opts.map(o => o.namesake), [true, true])
  assert.equal(opts[0].text, 'app (2 observations, last used 2026-10-01)')
  assert.deepEqual(opts.map(o => o.id), ['app_111111', 'app_222222'], 'ties on the name keep a stable order by id')
})

test('a project that only has summaries is marked', () => {
  const [o] = buildProjectOptions([row('notes_aaaaaa', 'notes', { sessions: 0, observations: 0, summaries: 4 })])
  assert.equal(o.note, 'summaries only')
})

test('a project with observations or sessions has no remark, and neither has a project of an older worker', () => {
  const [a, b] = buildProjectOptions([
    row('a_111111', 'a', { sessions: 0, observations: 2, summaries: 1 }),
    { project: 'b_222222', display_name: 'b', sessions: 0, observations: 0, last_active_epoch: 1 } // no summaries field
  ])
  assert.equal(a.note, '')
  assert.equal(b.note, '')
})

test('an alias says what it is an alias of', () => {
  const opts = buildProjectOptions([row('main_111111', 'main'), row('old_222222', 'old', { alias_of: 'main_111111' })])
  assert.equal(opts.find(o => o.id === 'old_222222').note, 'alias of main')
  const lone = buildProjectOptions([row('old_222222', 'old', { alias_of: 'gone_999999' })])
  assert.equal(lone[0].note, 'alias of gone_999999', 'falls back to the id when the target is not listed')
})

test('the list is alphabetical by name, ignoring case', () => {
  const names = buildProjectOptions([row('b_1', 'beta'), row('a_1', 'Alpha'), row('c_1', 'charlie')]).map(o => o.name)
  assert.deepEqual(names, ['Alpha', 'beta', 'charlie'])
})

test('a missing display name or label falls back to the id', () => {
  const [o] = buildProjectOptions([{ project: 'x_1', display_name: '', sessions: 0, observations: 0, last_active_epoch: 0 }])
  assert.equal(o.text, 'x_1')
  assert.equal(o.namesake, false)
})

test('search matches the name, the shown text and the id', () => {
  const [o] = buildProjectOptions([row('app_111111', 'app', { label: 'app (2 observations)' })])
  assert.ok(optionMatches(o, ''))
  assert.ok(optionMatches(o, '  '))
  assert.ok(optionMatches(o, 'APP'))
  assert.ok(optionMatches(o, 'observations'))
  assert.ok(optionMatches(o, '111111'), 'a pasted hash still finds the project')
  assert.ok(!optionMatches(o, 'zzz'))
})

test('the filter button text', () => {
  const opts = buildProjectOptions([row('claude-mnemonic_41bfcd', 'claude-mnemonic')])
  assert.equal(selectedText(opts, null), 'All Projects')
  assert.equal(selectedText(opts, 'claude-mnemonic_41bfcd'), 'claude-mnemonic')
  assert.equal(selectedText(opts, 'unknown_123456'), 'unknown_123456', 'an id that is not listed is shown as it is')
  assert.equal(selectedText(opts, '/a/b/path_1'), 'path_1', 'old path-style ids still show their last part')
})
