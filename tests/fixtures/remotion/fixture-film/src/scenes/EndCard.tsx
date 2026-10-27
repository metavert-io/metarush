import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { Pill } from "../components/Pill";
import { END } from "../story";
import { COLORS, FONT } from "../theme";

// The end card: its words come from the story unless the composition gives its own.
export const EndCard: React.FC<{ tagline?: string; cta?: string }> = ({ tagline = END.tagline, cta = END.cta }) => {
  const frame = useCurrentFrame();
  const rise = interpolate(frame, [0, 12], [24, 0], { extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.ground, alignItems: "center", justifyContent: "center", gap: 22, fontFamily: FONT }}>
      <div style={{ color: COLORS.ink, fontSize: 40, transform: `translateY(${rise}px)` }}>{tagline}</div>
      <Pill>{cta}</Pill>
    </AbsoluteFill>
  );
};
