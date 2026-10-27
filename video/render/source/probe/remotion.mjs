// What the project's own files get when they import "remotion" in a map run
// (D167): Remotion itself, with its compositions and sequences wrapped in
// MetaRush's probe (probe.mjs). Only the project's files see this; Remotion's
// own packages import the real one. Nothing is drawn differently.
import React from 'react';
import * as Real from 'remotion';
import { SceneProbe, childName, nameOf, onlySoundOf, probeChildren, withRoot, withRootLazy } from './probe.mjs';

export * from 'remotion';

const h = React.createElement;
const isSound = (t) => !!t && (t === Real.Audio || t === Real.Html5Audio || ['Audio', 'Html5Audio', 'AudioForRendering'].includes(nameOf(t)));

export const Composition = (props) => h(Real.Composition, { ...props, component: withRoot(props.component), lazyComponent: withRootLazy(props.lazyComponent) });
export const Still = (props) => h(Real.Still, { ...props, component: withRoot(props.component), lazyComponent: withRootLazy(props.lazyComponent) });

export const Sequence = React.forwardRef(function Sequence(props, ref) {
  const meta = { kind: 'sequence', index: 0, name: typeof props.name === 'string' ? props.name : '', component: childName(props.children), sound: onlySoundOf(props.children, isSound) };
  return h(Real.Sequence, { ...props, ref }, h(SceneProbe, { meta }, props.children));
});

const SeriesWrapped = (props) => h(Real.Series, props, probeChildren(props.children, (t) => t === Real.Series.Sequence, 'series', isSound));
SeriesWrapped.Sequence = Real.Series.Sequence;
export const Series = SeriesWrapped;
