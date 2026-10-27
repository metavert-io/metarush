#!/usr/bin/env node
// The live teleprompter's timing (web/src/screens/studio/voiceover/prompter.ts):
// pinned lines are cued about 2 s before their moment and become the line to say
// right on it; as-is lines follow each other at the narrator's pace from the start
// of the recording; Space (a manual advance) brings a line in early and moves the
// lines that follow it at their pace, never a pinned one.
//
//   node --experimental-strip-types web/scripts/prompter-test.mjs
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { schedule, advance, promptAt, CUE_LEAD, FIRST_LEAD } = await import(path.join(here, '..', 'src', 'screens', 'studio', 'voiceover', 'prompter.ts'));

let failed = 0;
let checked = 0;
const eq = (name, got, want) => {
  checked++;
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++;
    console.error(`FAIL ${name}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
};
const line = (n, pinned, start, dur) => ({ id: `line-${n}`, n, text: `Line ${n}`, pinned, start, dur });

// Pinned lines: due exactly at their pins, whatever the recording's start.
const pinned = [line(1, true, 4.2, 2), line(2, true, 9.75, 1.5), line(3, true, 14, 3)];
const due = schedule(pinned, 2);
eq('pinned lines are due at their pins', due, [4.2, 9.75, 14]);
// Before the first line: nothing to say yet, line 1 is next; its countdown starts CUE_LEAD (2 s) before.
eq('2.19 s: line 1 next, no countdown yet (it is 2.01 s away)', promptAt(due, 4.2 - CUE_LEAD - 0.01), { current: -1, next: 0, countdown: null });
eq('2.2 s: the countdown for line 1 starts', promptAt(due, 4.2 - CUE_LEAD), { current: -1, next: 0, countdown: 2 });
eq('3.7 s: half a second to go', promptAt(due, 3.7), { current: -1, next: 0, countdown: 0.5 });
eq('4.2 s: line 1 is the line to say, line 2 next', promptAt(due, 4.2), { current: 0, next: 1, countdown: null });
eq('7.75 s: line 2 cued 2 s before its pin', promptAt(due, 7.75), { current: 0, next: 1, countdown: 2 });
eq('9.75 s: line 2 on its pin', promptAt(due, 9.75), { current: 1, next: 2, countdown: null });
eq('20 s: the last line, nothing next', promptAt(due, 20), { current: 2, next: -1, countdown: null });
// A manual advance never moves a pinned line's moment for the others.
eq('Space on a pinned line brings only it in', advance(due, pinned, 1, 9), [4.2, 9, 14]);

// As-is lines (no timing yet): the first FIRST_LEAD after the recording starts, the rest at the pace.
const asIs = [line(1, false, 0, 2.5), line(2, false, 2.8, 1.8), line(3, false, 4.9, 3)];
const d2 = schedule(asIs, 6, 0.3);
eq('as-is lines start a second into the recording and follow each other at the pace', d2, [6 + FIRST_LEAD, 9.8, 11.9]);
eq('as-is: line 2 cued 2 s ahead', promptAt(d2, 7.8), { current: 0, next: 1, countdown: 2 });
// Space at 8.5 s brings line 2 in now; line 3 follows it at the pace.
const d3 = advance(d2, asIs, 1, 8.5);
eq('Space brings line 2 in now and line 3 moves with it', d3, [7, 8.5, 10.6]);
eq('after Space: line 2 is the line to say (line 3 is 2.1 s away: not cued yet)', promptAt(d3, 8.5), { current: 1, next: 2, countdown: null });

// Mixed: a line that follows a pinned one comes after it at the pace; Space moves it, not the next pin.
const mixed = [line(1, true, 3, 2), line(2, false, 5.3, 2), line(3, true, 12, 1)];
const d4 = schedule(mixed, 0, 0.3);
eq('a following line comes after the pinned one before it', d4, [3, 5.3, 12]);
eq('Space on the following line moves it but not the next pin', advance(d4, mixed, 1, 4.6), [3, 4.6, 12]);

// A punch-in from the middle: lines already passed are not cued again.
eq('recording from 10 s: line 2 (9.75) is under way, line 3 next', promptAt(due, 10), { current: 1, next: 2, countdown: null });

console.log(`${failed ? 'FAIL' : 'ok  '} prompter timing: ${checked - failed}/${checked} checks`);
if (failed) process.exit(1);
