// Drive the real dashboard in headless Chrome over the DevTools protocol.
import { spawn } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const [, , uiUrl, workerUrl, idsJson] = process.argv
const ids = JSON.parse(idsJson)
// The dropdown shows names, not hashes: the display name is the id without its trailing hash.
const nameOf = (id) => id.replace(/_[0-9a-f]{6}$/, '')
const hashOf = (id) => id.split('_').pop()
const CHROME = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PORT = 9333
let ok = 0, fail = 0
const check = (name, cond, detail = '') => { cond ? ok++ : fail++; console.log(`  ${cond ? 'PASS' : 'FAIL'}  ${name}${cond ? '' : '  ' + detail}`) }
const sleep = (ms) => new Promise(r => setTimeout(r, ms))

const chrome = spawn(CHROME, ['--headless=new', `--remote-debugging-port=${PORT}`, `--user-data-dir=${mkdtempSync(join(tmpdir(), 'chrome-'))}`,
  '--no-first-run', '--no-default-browser-check', '--window-size=1280,1000', 'about:blank'], { stdio: 'ignore' })
const cleanup = () => { try { chrome.kill() } catch {} }
process.on('exit', cleanup)

async function targetWs() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json()
      const page = list.find(t => t.type === 'page')
      if (page) return page.webSocketDebuggerUrl
    } catch {}
    await sleep(200)
  }
  throw new Error('chrome did not start')
}

const ws = new WebSocket(await targetWs())
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })
let seq = 0
const waiting = new Map()
ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && waiting.has(d.id)) { waiting.get(d.id)(d); waiting.delete(d.id) } }
const send = (method, params = {}) => new Promise(res => { const id = ++seq; waiting.set(id, res); ws.send(JSON.stringify({ id, method, params })) })
const evaluate = async (expression) => {
  const r = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })
  if (r.result.exceptionDetails) throw new Error(JSON.stringify(r.result.exceptionDetails.exception?.description ?? r.result.exceptionDetails))
  return r.result.result.value
}
const waitFor = async (expression, what, ms = 8000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { try { if (await evaluate(expression)) return true } catch {} await sleep(100) }
  throw new Error('timed out waiting for ' + what)
}
const text = () => evaluate('document.body.innerText')
const clickByText = (sel, label) => evaluate(`(() => { const el = [...document.querySelectorAll(${JSON.stringify(sel)})].find(e => e.innerText.trim().includes(${JSON.stringify(label)})); if (!el) return false; el.click(); return true })()`)
const clickAria = (label) => evaluate(`(() => { const el = document.querySelector('[aria-label=${JSON.stringify(label)}]'); if (!el) return false; el.click(); return true })()`)
const worker = async (path, init) => { const r = await fetch(workerUrl + path, init); return r.status === 204 ? null : r.json().catch(() => null) }

try {
  await send('Page.enable')
  await send('Page.navigate', { url: uiUrl })
  await waitFor(`!!document.querySelector('.project-filter button')`, 'project filter')

  console.log('== the dropdown lists every project that has data, by name')
  await evaluate(`document.querySelector('.project-filter button').click()`)
  await waitFor(`document.body.innerText.includes('Manage projects…') && document.querySelectorAll('.project-filter button[title]').length >= ${Object.keys(ids).length}`, 'dropdown items')
  const dropdownIds = await evaluate(`[...document.querySelectorAll('.project-filter button[title]')].map(b => b.title).sort()`)
  check('it lists exactly the seeded projects, including the one without a session and the one with only summaries',
    JSON.stringify(dropdownIds) === JSON.stringify(Object.values(ids).sort()), JSON.stringify(dropdownIds))
  const dropdownText = await evaluate(`document.querySelector('.project-filter .max-h-64').innerText`)
  check('unique names are shown without their hash', Object.values(ids).every(id => dropdownText.includes(nameOf(id)) && !dropdownText.includes(hashOf(id))),
    dropdownText.replace(/\n/g, ' | '))
  check('the project that only has summaries says so', /summ-only\s*\n?\s*summaries only/.test(dropdownText) || dropdownText.includes('summaries only'))
  check('every entry carries its id as a tooltip', await evaluate(`[...document.querySelectorAll('.project-filter button[title]')].every(b => /_[0-9a-f]{6}$/.test(b.title))`))
  await evaluate(`(() => { const i = document.querySelector('.project-filter input[type=text]'); i.value = ${JSON.stringify(hashOf(ids.main_proj))}; i.dispatchEvent(new Event('input', { bubbles: true })) })()`)
  await waitFor(`document.querySelectorAll('.project-filter button[title]').length === 1`, 'search by hash')
  check('a pasted hash still finds its project', (await evaluate(`document.querySelector('.project-filter button[title]').title`)) === ids.main_proj)
  await evaluate(`(() => { const i = document.querySelector('.project-filter input[type=text]'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })) })()`)
  await waitFor(`document.querySelectorAll('.project-filter button[title]').length >= ${Object.keys(ids).length}`, 'search cleared')

  console.log('== a project with only summaries can be selected')
  await clickByText('.project-filter button', nameOf(ids.summ_only))
  await waitFor(`document.querySelector('.project-filter > button').innerText.includes(${JSON.stringify(nameOf(ids.summ_only))})`, 'filter on the summary-only project')
  check('the filter shows its name', true)
  check('and does not break the page', !(await text()).includes('Failed to load'))
  await evaluate(`document.querySelector('.project-filter > button').click()`)
  await waitFor(`document.body.innerText.includes('All Projects') && !!document.querySelector('.project-filter button[title]')`, 'dropdown reopened')
  await clickByText('.project-filter button', 'All Projects')
  await waitFor(`document.querySelector('.project-filter > button').innerText.includes('All Projects')`, 'back to all projects')

  console.log('== open the manager from the project dropdown')
  await evaluate(`document.querySelector('.project-filter > button').click()`)
  await waitFor(`document.body.innerText.includes('Manage projects…')`, 'footer button')
  check('dropdown has a "Manage projects…" entry', true)
  await clickByText('.project-filter button', 'Manage projects')
  await waitFor(`document.body.innerText.includes('Merge a project into another, or delete it')`, 'manager modal')
  // The modal opens before its list has loaded: wait for the rows themselves, not for text that also appears in the timeline.
  await waitFor(`!!document.querySelector('[aria-label="Delete ${ids.doomed}"]') && document.body.innerText.includes('also old-fragment_abcdef')`, 'manager rows and aliases loaded')
  const rowIds = await evaluate(`[...document.querySelectorAll('[aria-label^="Delete "]')].map(b => b.getAttribute('aria-label').slice(7))`)
  check('every seeded project has a row in the manager', Object.values(ids).every(id => rowIds.includes(id)), JSON.stringify(rowIds))
  check('the manager lists exactly the projects the dropdown listed', JSON.stringify([...rowIds].sort()) === JSON.stringify(dropdownIds), JSON.stringify(rowIds))
  let t = await text()
  check('observation counts are shown in the rows', (t.match(/1 observations/g) ?? []).length >= 4)
  check('the alias is listed under Aliases and as a chip on its project', t.includes('old-fragment_abcdef') && t.includes('also old-fragment_abcdef'))

  console.log('== delete: preview first, nothing changes until confirmed')
  check('clicked Delete on the doomed project', await clickAria(`Delete ${ids.doomed}`))
  await waitFor(`document.body.innerText.includes('Delete ${ids.doomed}?')`, 'delete preview')
  t = await text()
  check('the preview explains it is permanent and backed up', /backup of the whole database/.test(t))
  check('the preview shows counts', /Observations/.test(t) && /Vectors/.test(t))
  check('the project still exists on the worker', (await worker(`/api/projects/${ids.doomed}/stats`)).observations === 1)

  console.log('== a change after the preview forces a re-confirm instead of deleting blindly')
  await worker('/api/observations/remember', { method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project: ids.doomed, text: 'Added while the preview was open.' }) })
  await clickByText('button', 'Delete project')
  await waitFor(`document.body.innerText.includes('changed since the preview')`, 'stale-token message')
  check('stale confirmation is refused with an explanation', true)
  check('nothing was deleted', (await worker(`/api/projects/${ids.doomed}/stats`)).observations === 2)
  t = await text()
  check('the preview now shows the current numbers', /Delete .*\?/.test(t) && t.includes('2') )

  console.log('== confirm with the fresh preview')
  await clickByText('button', 'Delete project')
  await waitFor(`document.body.innerText.includes('Deleted project ${ids.doomed}')`, 'delete result')
  check('success notice names the project and the backup', (await text()).includes('backup of the database before the change is at'))
  check('the worker no longer has it', (await fetch(`${workerUrl}/api/projects/${ids.doomed}/stats`)).status === 404)
  await waitFor(`!document.body.innerText.includes('${ids.doomed}') || document.body.innerText.includes('Deleted project ${ids.doomed}')`, 'list refresh')
  check('the row is gone from the list', !(await evaluate(`!!document.querySelector('[aria-label="Delete ${ids.doomed}"]')`)))

  console.log('== merge: pick a target, preview, confirm')
  check('clicked Merge on the fragment', await clickAria(`Merge ${ids.fragment}`))
  await waitFor(`document.body.innerText.includes('Merge ${ids.fragment} into:')`, 'target picker')
  check('the picker does not offer the source itself', !(await evaluate(`[...document.querySelectorAll('select[aria-label="Merge target"] option')].some(o => o.value === ${JSON.stringify(ids.fragment)})`)))
  check('Preview is disabled until a target is chosen', await evaluate(`[...document.querySelectorAll('button')].find(b => b.innerText.trim() === 'Preview').disabled`))
  await evaluate(`(() => { const s = document.querySelector('select[aria-label="Merge target"]'); s.value = ${JSON.stringify(ids.main_proj)}; s.dispatchEvent(new Event('change', { bubbles: true })) })()`)
  await waitFor(`!([...document.querySelectorAll('button')].find(b => b.innerText.trim() === 'Preview').disabled)`, 'preview enabled')
  await clickByText('button', 'Preview')
  await waitFor(`document.body.innerText.includes('Merge ${ids.fragment} into ${ids.main_proj}?')`, 'merge preview')
  t = await text()
  check('merge preview says embeddings are kept and the old id keeps resolving', /Embeddings are kept/.test(t) && t.includes('will keep resolving'))
  check('nothing moved yet', (await worker(`/api/projects/${ids.fragment}/stats`)).observations === 1)
  await clickByText('button', 'Merge projects')
  await waitFor(`document.body.innerText.includes('Merged ${ids.fragment} into ${ids.main_proj}')`, 'merge result')
  check('the survivor now holds both notes', (await worker(`/api/projects/${ids.main_proj}/stats`)).observations === 2)
  const resolved = await worker(`/api/projects/resolve?id=${ids.fragment}`)
  check('the old id resolves to the survivor', resolved.id === ids.main_proj && resolved.match === 'alias', JSON.stringify(resolved))
  await waitFor(`document.body.innerText.includes('also ${ids.fragment}')`, 'alias chip on survivor')
  check('the survivor shows the merged id as an alias', true)

  console.log('== remove an alias from the list')
  check('clicked remove on the manual alias', await clickAria('Remove alias old-fragment_abcdef'))
  await waitFor(`!document.body.innerText.includes('also old-fragment_abcdef')`, 'alias removed')
  check('alias is gone on the worker', (await worker('/api/projects/aliases')).every(a => a.alias !== 'old-fragment_abcdef'))

  console.log('== cancelling leaves everything alone')
  await clickAria(`Delete ${ids.spare}`)
  await waitFor(`document.body.innerText.includes('Delete ${ids.spare}?')`, 'second preview')
  await clickByText('button', 'Cancel')
  await waitFor(`!document.body.innerText.includes('Delete ${ids.spare}?')`, 'preview closed')
  check('cancel closes the preview and deletes nothing', (await worker(`/api/projects/${ids.spare}/stats`)).observations === 1)

  console.log('== closing the modal')
  await evaluate(`document.querySelector('[aria-label="Close"]').click()`)
  await waitFor(`!document.body.innerText.includes('Merge a project into another, or delete it')`, 'modal closed')
  check('modal closes', true)

  console.log('== deleting the project the filter is on falls back to all projects')
  const triggerText = () => evaluate(`document.querySelector('.project-filter > button').innerText`)
  check('filter starts on all projects', (await triggerText()).includes('All Projects'))
  await evaluate(`document.querySelector('.project-filter > button').click()`)
  await waitFor(`[...document.querySelectorAll('.project-filter button[title]')].some(b => b.title === ${JSON.stringify(ids.spare)})`, 'spare in dropdown')
  await clickByText('.project-filter button', nameOf(ids.spare))
  await waitFor(`document.querySelector('.project-filter > button').innerText.includes(${JSON.stringify(nameOf(ids.spare))})`, 'filter on spare')
  check('filter now shows the spare project', true)
  await evaluate(`document.querySelector('.project-filter > button').click()`)
  await waitFor(`document.body.innerText.includes('Manage projects…')`, 'footer')
  await clickByText('.project-filter button', 'Manage projects')
  await waitFor(`!!document.querySelector('[aria-label="Delete ${ids.spare}"]')`, 'manager reopened')
  await clickAria(`Delete ${ids.spare}`)
  await waitFor(`document.body.innerText.includes('Delete ${ids.spare}?')`, 'spare preview')
  await clickByText('button', 'Delete project')
  await waitFor(`document.body.innerText.includes('Deleted project ${ids.spare}')`, 'spare deleted')
  await evaluate(`document.querySelector('[aria-label="Close"]').click()`)
  await waitFor(`document.querySelector('.project-filter > button').innerText.includes('All Projects')`, 'filter reset')
  check('the filter fell back to All Projects instead of pointing at a deleted project', true)
  const dropdownGone = await evaluate(`(async () => { document.querySelector('.project-filter > button').click(); await new Promise(r => setTimeout(r, 800)); return ![...document.querySelectorAll('.project-filter button[title]')].some(b => b.title === ${JSON.stringify(ids.spare)}) })()`)
  check('and the dropdown list was re-read (no stale cached entry)', dropdownGone)

  console.log('== the Conflicts tab: review proposals side by side')
  const openConflicts = async () => (await worker('/api/conflicts?status=open')).conflicts
  const injectedIds = async () => ((await worker(`/api/context/inject?project=${ids.reviewed}`)).observations ?? []).map(o => o.id)
  const panelCount = () => evaluate(`document.querySelectorAll('[data-testid=conflict-item]').length`)
  const press = (key) => evaluate(`window.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(key)}, bubbles: true }))`)
  const baseline = (await injectedIds()).sort()
  const proposals = await openConflicts()
  check('two proposals were seeded', proposals.length === 2, String(proposals.length))
  await waitFor(`document.querySelector('[data-testid=conflict-badge]')?.innerText.trim() === '2'`, 'badge with two')
  check('the Conflicts tab carries a badge with the number waiting', true)
  check('opened the Conflicts tab', await clickByText('button', 'Conflicts'))
  await waitFor(`!!document.querySelector('[data-testid=conflicts-panel]') && document.querySelectorAll('[data-testid=conflict-item]').length === 2`, 'queue with two items')
  check('the queue lists both proposals', (await panelCount()) === 2)
  await waitFor(`!!document.querySelector('[data-testid=conflict-detail]')`, 'detail')
  const detail = await evaluate(`document.querySelector('[data-testid=conflict-detail]').innerText`)
  check('the first proposal is open with older and newer side by side', /older\s*·\s*#\d+/i.test(detail) && /newer\s*·\s*#\d+/i.test(detail), detail.slice(0, 300))
  check('the reason is shown', (await evaluate(`document.querySelector('[data-testid=conflict-reason]')?.innerText ?? ''`)).startsWith('The lifetime changed'))
  check('what differs is marked on both sides', await evaluate(`!!document.querySelector('[data-testid=conflict-detail] [class*="bg-red-500/25"]') && !!document.querySelector('[data-testid=conflict-detail] [class*="bg-emerald-500/25"]')`))
  check('all three decisions and skip are offered', await evaluate(`['supersede_older','supersede_newer','keep_both'].every(d => !!document.querySelector('[data-testid=decision-' + d + ']')) && !!document.querySelector('[data-testid=conflict-skip]')`))
  const first = await evaluate(`document.querySelector('[data-testid=conflict-detail]').innerText`)
  await press('j')
  await waitFor(`document.querySelector('[data-testid=conflict-detail]').innerText !== ${JSON.stringify(first)}`, 'next proposal')
  check('j moves to the next proposal', true)
  await press('k')
  await waitFor(`document.querySelector('[data-testid=conflict-detail]').innerText === ${JSON.stringify(first)}`, 'previous proposal')
  check('k moves back', true)

  console.log('== decide on click, undo from the toast')
  const target = proposals.find(c => first.includes('#' + c.older.id) && first.includes('#' + c.newer.id))
  check('found the proposal on screen among the seeded ones', !!target)
  await evaluate(`document.querySelector('[data-testid=decision-supersede_older]').click()`)
  await waitFor(`!!document.querySelector('[data-testid=conflict-toast]')`, 'undo toast')
  check('the toast offers an undo', (await evaluate(`document.querySelector('[data-testid=conflict-toast]').innerText`)).includes('Newer replaces older'))
  check('the worker has the decision', (await openConflicts()).length === 1 && !(await openConflicts()).some(c => c.id === target.id))
  check('the older note is hidden from sessions', !(await injectedIds()).includes(target.older.id))
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 1`, 'queue with one')
  await waitFor(`document.querySelector('[data-testid=conflict-badge]')?.innerText.trim() === '1'`, 'badge with one')
  check('the queue and the badge went down by one', true)
  await evaluate(`document.querySelector('[data-testid=conflict-toast-undo]').click()`)
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 2`, 'queue with two again')
  check('undo brings the proposal back', (await openConflicts()).length === 2)
  check('and what sessions see is what it was before the decision', JSON.stringify((await injectedIds()).sort()) === JSON.stringify(baseline), JSON.stringify(await injectedIds()))

  console.log('== decide from the keyboard, then look at and undo the decision')
  await press('3')
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 1`, 'keep both applied')
  check('key 3 keeps both', (await openConflicts()).length === 1)
  check('clicked the Decided list', await evaluate(`(() => { document.querySelector('[data-testid=conflict-status-resolved]').click(); return true })()`))
  await waitFor(`!!document.querySelector('[data-testid=conflict-decided]')`, 'decided detail')
  check('the decision is shown', (await evaluate(`document.querySelector('[data-testid=conflict-decided]').innerText`)).includes('Keep both'))
  await evaluate(`document.querySelector('[data-testid=conflict-undo]').click()`)
  await waitFor(`document.querySelector('[data-testid=conflict-empty]')?.innerText.includes('No decisions yet')`, 'decided list empty')
  check('undo from the Decided list reopens it', (await openConflicts()).length === 2)

  console.log('== a decided note is marked in the timeline')
  await evaluate(`document.querySelector('[data-testid=conflict-status-open]').click()`)
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 2`, 'open list again')
  await press('1')
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 1`, 'decided with 1')
  check('clicked the All tab', await evaluate(`(() => { const b = [...document.querySelectorAll('button')].find(e => e.innerText.trim() === 'All'); if (!b) return false; b.click(); return true })()`))
  await waitFor(`document.body.innerText.includes('SUPERSEDED')`, 'superseded badge', 10000)
  check('the superseded note shows a badge in the timeline', true)
  await clickByText('button', 'Conflicts')

  console.log('== nothing left to review')
  await waitFor(`document.querySelectorAll('[data-testid=conflict-item]').length === 1`, 'one left')
  await press('3')
  await waitFor(`!!document.querySelector('[data-testid=conflict-empty]')`, 'empty state')
  check('the empty state says so', (await evaluate(`document.querySelector('[data-testid=conflict-empty]').innerText`)).includes('Nothing to review'))
  check('and the badge is gone', await evaluate(`!document.querySelector('[data-testid=conflict-badge]')`))
} catch (e) {
  fail++; console.log('  FAIL  ' + e.message)
  const diag = await evaluate(`JSON.stringify({
    rows: [...document.querySelectorAll('[aria-label^="Delete "]')].map(b => b.getAttribute('aria-label')),
    alerts: [...document.querySelectorAll('[role=alert],[role=status]')].map(e => e.innerText),
    loadingShown: document.body.innerText.includes('Loading projects'),
    modal: document.body.innerText.includes('Merge a project into another'),
    chips: document.body.innerText.includes('also old-fragment_abcdef')
  })`).catch(() => 'n/a')
  console.log('--- diagnostics at failure: ' + diag)
} finally {
  console.log(`\n${ok} passed, ${fail} failed`)
  cleanup()
  process.exit(fail ? 1 : 0)
}
