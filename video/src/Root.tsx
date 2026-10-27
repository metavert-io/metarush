import React from 'react';
import { Composition, Folder } from 'remotion';
import { DEMO_PLAN_LANDSCAPE, DEMO_PLAN_SQUARE, DEMO_PLAN_VERTICAL } from './demo/demoPlan';
import { MetaRushEdit } from './MetaRushEdit';
import { calculatePlanMetadata } from './metadata';
import { installMotionHost } from './motion/host';
import { MotionInfoView, calculateMotionInfo } from './motion/MotionInfo';

// The render page hosts project motion graphics (D68); the dashboard never imports this file.
installMotionHost();

/**
 * `MetaRushEdit` is the only composition the worker renders; its metadata
 * always comes from `props.plan`. The demo compositions are Studio-only
 * conveniences for checking template layouts per aspect ratio.
 */
export const RemotionRoot: React.FC = () => (
  <>
    <Composition
      id="MetaRushEdit"
      component={MetaRushEdit}
      defaultProps={{ plan: DEMO_PLAN_VERTICAL }}
      calculateMetadata={calculatePlanMetadata}
      width={DEMO_PLAN_VERTICAL.width}
      height={DEMO_PLAN_VERTICAL.height}
      fps={DEMO_PLAN_VERTICAL.fps}
      durationInFrames={DEMO_PLAN_VERTICAL.durationInFrames}
    />
    <Composition
      id="MetaRushMotionInfo"
      component={MotionInfoView}
      defaultProps={{ hash: '', url: '', preview: {} }}
      calculateMetadata={calculateMotionInfo}
      width={16}
      height={16}
      fps={30}
      durationInFrames={1}
    />
    <Folder name="Demos">
      <Composition
        id="Demo-Landscape"
        component={MetaRushEdit}
        defaultProps={{ plan: DEMO_PLAN_LANDSCAPE }}
        calculateMetadata={calculatePlanMetadata}
        width={DEMO_PLAN_LANDSCAPE.width}
        height={DEMO_PLAN_LANDSCAPE.height}
        fps={DEMO_PLAN_LANDSCAPE.fps}
        durationInFrames={DEMO_PLAN_LANDSCAPE.durationInFrames}
      />
      <Composition
        id="Demo-Square"
        component={MetaRushEdit}
        defaultProps={{ plan: DEMO_PLAN_SQUARE }}
        calculateMetadata={calculatePlanMetadata}
        width={DEMO_PLAN_SQUARE.width}
        height={DEMO_PLAN_SQUARE.height}
        fps={DEMO_PLAN_SQUARE.fps}
        durationInFrames={DEMO_PLAN_SQUARE.durationInFrames}
      />
    </Folder>
  </>
);
