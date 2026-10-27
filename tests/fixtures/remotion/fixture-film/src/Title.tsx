import React from "react";
import { AbsoluteFill, Audio, Img, interpolate, staticFile, useCurrentFrame } from "remotion";
import { HEADLINE, SUBTITLE } from "./story";
import { COLORS, FONT } from "./theme";

// The title: a picture from public/, the project's font, a sound from public/.
export const Title: React.FC = () => {
  const frame = useCurrentFrame();
  const opacity = interpolate(frame, [0, 15], [0, 1], { extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.ground, alignItems: "center", justifyContent: "center", fontFamily: FONT }}>
      <Img src={staticFile("picture.png")} style={{ width: 96, height: 96, marginBottom: 16, opacity }} />
      <div style={{ color: COLORS.ink, fontSize: 44, opacity }}>{HEADLINE}</div>
      <div style={{ color: COLORS.accent, fontSize: 22, marginTop: 10 }}>{SUBTITLE}</div>
      <Audio src={staticFile("tone.wav")} volume={0.5} />
    </AbsoluteFill>
  );
};
