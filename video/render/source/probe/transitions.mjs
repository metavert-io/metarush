// What the project's own files get when they import "@remotion/transitions" in
// a map run (D167): the package itself, with each TransitionSeries.Sequence's
// content wrapped in MetaRush's probe (probe.mjs), so the map knows where every
// scene of a transition series starts and how long it is. Nothing is drawn
// differently.
import React from 'react';
import * as Real from '@remotion/transitions';
import * as Remotion from 'remotion';
import { nameOf, probeChildren } from './probe.mjs';

export * from '@remotion/transitions';

const isSound = (t) => !!t && (t === Remotion.Audio || t === Remotion.Html5Audio || ['Audio', 'Html5Audio'].includes(nameOf(t)));
const Wrapped = (props) => React.createElement(Real.TransitionSeries, props, probeChildren(props.children, (t) => t === Real.TransitionSeries.Sequence, 'transition-series', isSound));
for (const k of Object.keys(Real.TransitionSeries)) Wrapped[k] = Real.TransitionSeries[k];
Wrapped.Sequence = Real.TransitionSeries.Sequence;
Wrapped.Transition = Real.TransitionSeries.Transition;
if (Real.TransitionSeries.Overlay) Wrapped.Overlay = Real.TransitionSeries.Overlay;
export const TransitionSeries = Wrapped;
