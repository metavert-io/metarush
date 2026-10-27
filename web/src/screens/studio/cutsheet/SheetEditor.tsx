import { useEffect, useImperativeHandle, useRef, type Ref } from 'react';
import { Annotation, Compartment, EditorState, type Range } from '@codemirror/state';
import { Decoration, EditorView, ViewPlugin, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { markdownLanguage } from '@codemirror/lang-markdown';
import { LanguageSupport, syntaxHighlighting } from '@codemirror/language';
import { classHighlighter } from '@lezer/highlight';
import { lintGutter, setDiagnostics, type Diagnostic } from '@codemirror/lint';
import type { CutProblem } from '../../../api/types';
import { problemRange } from './positions';

/*
 * The cut sheet editor (loaded on demand: CodeMirror stays out of the main
 * bundle). Markdown highlighting comes from the language package; the cut
 * sheet's own pieces (times, ↳ transitions, clip and footage names, quoted
 * words, the derived > lines, the settings block) get marks of their own.
 */

export interface SheetEditorHandle {
  /** Put the cursor at a line (1-based) and column, scroll there and focus. */
  focusLine(line: number, col?: number): void;
}

export interface SheetEditorProps {
  value: string;
  readOnly: boolean;
  problems: CutProblem[];
  onChange: (text: string) => void;
  label: string;
  handleRef?: Ref<SheetEditorHandle>;
}

const external = Annotation.define<boolean>();

const mark = (cls: string) => Decoration.mark({ class: cls });
const lineDeco = (cls: string) => Decoration.line({ class: cls });

const M = {
  settings: lineDeco('cm-cs-settings'),
  key: mark('cm-cs-key'),
  comment: mark('cm-cs-comment'),
  time: mark('cm-cs-time'),
  clip: mark('cm-cs-clip'),
  footage: mark('cm-cs-footage'),
  num: mark('cm-cs-num'),
  transLine: lineDeco('cm-cs-transition-line'),
  trans: mark('cm-cs-transition'),
  phrase: mark('cm-cs-phrase'),
  derived: lineDeco('cm-cs-derived'),
  label: mark('cm-cs-label'),
  sep: mark('cm-cs-sep'),
};

const TIME = /(?<![\w:.])\d+:\d{2}(?::\d{2})?(?:\.\d+)?(?![\w:])/g;
const QUOTED = /"(?:[^"\\]|\\.)*"|“[^”]*”/g;
/** Shot names in a narration line's start point: "when hook ends", "0.5 s after hook starts", "in hook". */
const SHOT_REF = /\b(?:when|after|before|in)\s+([a-z][a-z0-9_-]*)/g;
/** Footage in a start point at a moment: "at interview-a 0:12.3". */
const FOOTAGE_REF = /\bat\s+([^\s·"“]+)\s+\d/g;

function decorate(view: EditorView): DecorationSet {
  const doc = view.state.doc;
  const out: Range<Decoration>[] = [];
  let settingsEnd = 0;
  if (doc.lines > 1 && doc.line(1).text.trim() === '---') {
    for (let n = 2; n <= doc.lines; n++) {
      if (doc.line(n).text.trimEnd() === '---') {
        settingsEnd = n;
        break;
      }
    }
  }
  // Names a narration line can point at: the clips' names and the footage names.
  const shots = new Set<string>();
  const footage = new Set<string>();
  let inFootage = false;
  for (let n = 1; n <= doc.lines; n++) {
    const t = doc.line(n).text;
    if (settingsEnd && n <= settingsEnd) {
      if (/^footage:\s*$/.test(t)) inFootage = true;
      else if (inFootage && /^\s+[^:#\s]+:/.test(t)) footage.add(/^\s+([^:#\s]+):/.exec(t)![1]!);
      else inFootage = false;
      continue;
    }
    const c = /^\s*\d+[.)]\s+([a-z][a-z0-9_-]*):/.exec(t);
    if (c) shots.add(c[1]!);
  }
  for (let n = 1; n <= doc.lines; n++) {
    const line = doc.line(n);
    const t = line.text;
    if (settingsEnd && n <= settingsEnd) {
      out.push(M.settings.range(line.from));
      const k = /^(\s*)([^:#\s][^:#]*?):(\s|$)/.exec(t);
      if (k) out.push(M.key.range(line.from + k[1]!.length, line.from + k[1]!.length + k[2]!.length));
      const c = t.search(/\s#\s/);
      if (c >= 0) out.push(M.comment.range(line.from + c + 1, line.to));
      continue;
    }
    if (/^\s*>/.test(t)) {
      out.push(M.derived.range(line.from));
      continue;
    }
    const tr = /^(\s*)(↳|=>)(.*)$/.exec(t);
    if (tr) {
      out.push(M.transLine.range(line.from));
      out.push(M.trans.range(line.from + tr[1]!.length, line.to));
      continue;
    }
    const clip = /^(\s*)(\d+[.)])(\s+)([a-z][a-z0-9_-]*:)?(\s*)(\S+)?/.exec(t);
    const bullet = !clip ? /^(\s*)([-*+])(\s+)([a-z][a-z0-9_-]*:)?(\s*)(\S+)?/.exec(t) : null;
    const item = clip ?? bullet;
    if (item) {
      let at = line.from + item[1]!.length;
      if (clip) out.push(M.num.range(at, at + item[2]!.length));
      at += item[2]!.length + item[3]!.length;
      if (item[4]) {
        out.push(M.clip.range(at, at + item[4].length - 1));
        at += item[4].length + item[5]!.length;
        if (item[6] && !/^\d/.test(item[6]) && !item[6].startsWith('"')) out.push(M.footage.range(at, at + item[6].length));
      }
    }
    const lab = /^(\s*)(Why|Evidence|Rights|Notes|Delivery|Take|Recording):/i.exec(t);
    if (lab) out.push(M.label.range(line.from + lab[1]!.length, line.from + lab[1]!.length + lab[2]!.length + 1));
    for (const m of t.matchAll(TIME)) out.push(M.time.range(line.from + m.index!, line.from + m.index! + m[0].length));
    if (item) for (const m of t.matchAll(QUOTED)) out.push(M.phrase.range(line.from + m.index!, line.from + m.index! + m[0].length));
    if (bullet) {
      // Quoted words are the line itself, not names: blank them out first.
      const rest = t.replace(QUOTED, (q) => ' '.repeat(q.length));
      for (const m of rest.matchAll(SHOT_REF)) {
        if (!shots.has(m[1]!)) continue;
        const at = line.from + m.index! + m[0].length - m[1]!.length;
        out.push(M.clip.range(at, at + m[1]!.length));
      }
      for (const m of rest.matchAll(FOOTAGE_REF)) {
        if (!footage.has(m[1]!)) continue;
        const at = line.from + m.index! + m[0].indexOf(m[1]!, 2);
        out.push(M.footage.range(at, at + m[1]!.length));
      }
    }
    for (let i = t.indexOf('·'); i >= 0; i = t.indexOf('·', i + 1)) out.push(M.sep.range(line.from + i, line.from + i + 1));
  }
  return Decoration.set(out, true);
}

const sheetMarks = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = decorate(view);
    }
    update(u: ViewUpdate) {
      if (u.docChanged) this.decorations = decorate(u.view);
    }
  },
  { decorations: (v) => v.decorations },
);

const theme = EditorView.theme({
  '&': { height: '100%', fontSize: '13px', color: 'var(--fg)', backgroundColor: 'var(--bg-panel)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.65', overscrollBehavior: 'contain' },
  '.cm-content': { padding: '14px 0 40px', caretColor: 'var(--accent)' },
  '.cm-line': { padding: '0 16px 0 10px' },
  '.cm-gutters': { backgroundColor: 'var(--bg-raised)', color: 'var(--fg-subtle)', border: 'none', borderRight: '1px solid var(--line)' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 8px 0 12px', minWidth: '34px', fontSize: '11.5px' },
  '.cm-activeLine': { backgroundColor: 'color-mix(in oklab, var(--bg-hover) 55%, transparent)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--bg-hover)', color: 'var(--fg-muted)' },
  '.cm-cursor': { borderLeftColor: 'var(--accent)', borderLeftWidth: '2px' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': { backgroundColor: 'var(--accent-soft) !important' },
  '.cm-cs-settings': { backgroundColor: 'color-mix(in oklab, var(--bg-hover) 45%, transparent)' },
  '.cm-cs-settings .tok-heading, .cm-cs-settings .tok-meta': { fontWeight: 'inherit', fontSize: 'inherit', color: 'inherit' },
  '.cm-cs-key': { color: 'var(--fg-muted)' },
  '.cm-cs-comment, .cm-cs-derived': { color: 'var(--fg-subtle)', fontStyle: 'italic' },
  '.cm-cs-time': { color: 'var(--accent-text)', fontVariantNumeric: 'tabular-nums' },
  '.cm-cs-clip': { color: 'var(--fg)', fontWeight: '600' },
  '.cm-cs-num': { color: 'var(--fg-subtle)' },
  '.cm-cs-footage': { color: 'var(--info)' },
  '.cm-cs-transition-line': { backgroundColor: 'var(--accent-soft)', boxShadow: 'inset 2px 0 0 var(--accent-line)' },
  '.cm-cs-transition': { color: 'var(--accent-text)', fontStyle: 'italic' },
  '.cm-cs-phrase': { color: 'var(--ok)' },
  '.cm-cs-label': { color: 'var(--fg-muted)', fontWeight: '600' },
  '.cm-cs-sep': { color: 'var(--fg-subtle)' },
  '.tok-heading': { color: 'var(--fg)', fontWeight: '650' },
  '.tok-emphasis': { fontStyle: 'italic' },
  '.tok-strong': { fontWeight: '650' },
  '.tok-link, .tok-url': { color: 'var(--info)' },
  '.tok-meta, .tok-punctuation': { color: 'var(--fg-subtle)' },
  '.cm-lintRange': { backgroundImage: 'none', textDecorationLine: 'underline', textDecorationStyle: 'wavy', textUnderlineOffset: '4px', textDecorationSkipInk: 'none' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecorationColor: 'var(--danger)', backgroundColor: 'var(--danger-soft)' },
  '.cm-lintRange-warning, .cm-lintRange-info': { backgroundImage: 'none', textDecorationColor: 'var(--accent)' },
  '.cm-gutter-lint': { width: '14px' },
  '.cm-lint-marker': { width: '9px', height: '9px', margin: '4px 2px', borderRadius: '50%', content: 'none' },
  '.cm-lint-marker-error': { backgroundColor: 'var(--danger)' },
  '.cm-lint-marker-warning, .cm-lint-marker-info': { backgroundColor: 'var(--accent)' },
  '.cm-tooltip': { backgroundColor: 'var(--bg-raised)', color: 'var(--fg)', border: '1px solid var(--line-strong)', borderRadius: '10px', boxShadow: 'var(--shadow-soft)' },
  '.cm-tooltip-lint': { padding: '4px 0', maxWidth: 'min(460px, 90vw)' },
  '.cm-diagnostic': { padding: '6px 12px', fontFamily: 'var(--font-sans)', fontSize: '12.5px', lineHeight: '1.5', borderLeft: '3px solid transparent' },
  '.cm-diagnostic-error': { borderLeftColor: 'var(--danger)' },
  '.cm-diagnostic-warning, .cm-diagnostic-info': { borderLeftColor: 'var(--accent)' },
  '.cm-diagnosticText': { whiteSpace: 'pre-wrap' },
});

function diagnostics(state: EditorState, problems: CutProblem[]): Diagnostic[] {
  const out: Diagnostic[] = [];
  for (const p of problems) {
    const n = p.line && p.line <= state.doc.lines ? p.line : 1;
    const line = state.doc.line(n);
    const r = p.line ? problemRange({ from: line.from, text: line.text }, p.col, p.endCol) : { from: 0, to: 0 };
    out.push({ from: r.from, to: r.to, severity: p.severity === 'error' ? 'error' : 'warning', message: p.hint ? `${p.message}\n${p.hint}` : p.message });
  }
  return out;
}

export default function SheetEditor({ value, readOnly, problems, onChange, label, handleRef }: SheetEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const editable = useRef(new Compartment());
  const changeRef = useRef(onChange);
  changeRef.current = onChange;

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightActiveLine(),
          drawSelection(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          new LanguageSupport(markdownLanguage),
          syntaxHighlighting(classHighlighter),
          sheetMarks,
          lintGutter(),
          EditorView.lineWrapping,
          theme,
          EditorView.contentAttributes.of({ 'aria-label': label, spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' }),
          editable.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !u.transactions.some((tr) => tr.annotation(external))) changeRef.current(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The view is created once; value/readOnly/problems are synced below.
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const v = view.current;
    if (!v || v.state.doc.toString() === value) return;
    v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value }, annotations: external.of(true) });
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({ effects: editable.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) });
  }, [readOnly]);

  useEffect(() => {
    const v = view.current;
    if (v) v.dispatch(setDiagnostics(v.state, diagnostics(v.state, problems)));
  }, [problems, value]);

  useImperativeHandle(
    handleRef,
    () => ({
      focusLine(line: number, col?: number) {
        const v = view.current;
        if (!v) return;
        const l = v.state.doc.line(Math.min(Math.max(1, line), v.state.doc.lines));
        const pos = col ? problemRange({ from: l.from, text: l.text }, col).from : l.from;
        v.dispatch({ selection: { anchor: pos }, scrollIntoView: true });
        v.focus();
      },
    }),
    [],
  );

  return <div ref={host} className="h-full min-h-0" data-testid="cut-sheet-editor" />;
}
