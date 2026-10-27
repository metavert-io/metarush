import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { OPENING } from "../story";
import { COLORS, FONT } from "../theme";

// The opening: a line and a note, fading in.
export const Opening: React.FC = () => {
  const frame = useCurrentFrame();
  const opacity = interpolate(frame, [0, 10], [0, 1], { extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.ground, alignItems: "center", justifyContent: "center", fontFamily: FONT, opacity }}>
      <div style={{ color: COLORS.ink, fontSize: 42 }}>{OPENING.line}</div>
      <div style={{ color: COLORS.accent, fontSize: 22, marginTop: 10 }}>{OPENING.note}</div>
    </AbsoluteFill>
  );
};
