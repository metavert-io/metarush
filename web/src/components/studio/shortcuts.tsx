import { Kbd } from '../../ui/primitives';

export const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
export const mod = isMac ? '⌘' : 'Ctrl';
export const alt = isMac ? '⌥' : 'Alt';

/** Every timeline shortcut, in plain words (the "?" sheet). */
export const SHORTCUTS: { group: string; items: { keys: string[]; what: string }[] }[] = [
  {
    group: 'Playing',
    items: [
      { keys: ['Space'], what: 'Play or pause' },
      { keys: ['J', 'K', 'L'], what: 'Play backwards, stop, play forwards (press J or L again to go faster)' },
      { keys: ['←', '→'], what: 'Move the playhead one frame (with ⇧: one second)' },
      { keys: ['↑', '↓'], what: 'Jump to the previous or next cut' },
      { keys: ['Home', 'End'], what: 'Jump to the start or the end' },
    ],
  },
  {
    group: 'Editing',
    items: [
      { keys: ['S'], what: 'Split the clip under the playhead' },
      { keys: ['['], what: 'Trim the start of the clip under the playhead to the playhead' },
      { keys: [']'], what: 'Trim the end of the clip under the playhead to the playhead' },
      { keys: ['Delete'], what: 'Remove the selected clip, or the marked stretch' },
      { keys: ['I', 'O'], what: 'Mark where a stretch starts and ends (at the playhead)' },
      { keys: [`${mod} ←`, `${mod} →`], what: 'Move the selected clip one place earlier or later' },
      { keys: [`${alt} ←`, `${alt} →`], what: 'Slide the footage inside the selected clip by a frame (with ⇧: a second)' },
      { keys: [`${mod} Z`], what: 'Undo' },
      { keys: [`⇧ ${mod} Z`], what: 'Redo' },
    ],
  },
  {
    group: 'Trimming with the keyboard',
    items: [
      { keys: ['Tab'], what: 'Go to a clip, its start or end edge, or the cut between two clips' },
      { keys: ['←', '→'], what: 'On an edge: move it one frame (with ⇧: one second)' },
      { keys: [`${alt} ←`, `${alt} →`], what: 'On an edge: move the cut instead, the clip next to it changes too' },
      { keys: ['Enter'], what: 'On a cut: choose the transition' },
    ],
  },
  {
    group: 'In the transcript (beside the player)',
    items: [
      { keys: ['Click', '⇧ Click'], what: 'Go to a word; select up to a word (or drag across words)' },
      { keys: ['⇧ ←', '⇧ →'], what: 'Select one more word, or one fewer' },
      { keys: ['Delete'], what: 'Cut the selected words out, in the pauses around them' },
      { keys: ['Enter'], what: 'Play from the selected words' },
      { keys: ['Esc'], what: 'Clear the selection' },
    ],
  },
  {
    group: 'Seeing',
    items: [
      { keys: ['=', '-'], what: 'Zoom in or out' },
      { keys: ['\\'], what: 'Fit the whole cut' },
      { keys: ['⇧ (while dragging)'], what: 'Turn snapping off' },
      { keys: [`${alt} (while dragging an edge)`], what: 'Move the cut between two clips instead of trimming one' },
      { keys: ['?'], what: 'Show these shortcuts' },
    ],
  },
];

export function ShortcutSheet() {
  return (
    <div className="space-y-5">
      <p className="text-[13px] leading-relaxed text-muted">
        Everything you can drag on the timeline also has a key, so the whole cut can be edited from the keyboard. Changes save by themselves a moment after you stop.
      </p>
      {SHORTCUTS.map((g) => (
        <section key={g.group}>
          <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-[0.06em] text-subtle">{g.group}</h3>
          <ul className="divide-y divide-line overflow-hidden rounded-[12px] hairline">
            {g.items.map((it) => (
              <li key={it.what} className="flex items-center gap-3 px-3 py-2 text-[13px]">
                <span className="flex min-w-[118px] shrink-0 flex-wrap gap-1">
                  {it.keys.map((k) => (
                    <Kbd key={k}>{k}</Kbd>
                  ))}
                </span>
                <span className="text-fg">{it.what}</span>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}
