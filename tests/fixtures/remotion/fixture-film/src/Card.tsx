import React from "react";
import { AbsoluteFill, useCurrentFrame, useVideoConfig } from "remotion";
import { z } from "zod";
import { COLORS, FONT } from "./theme";

export const cardSchema = z.object({ headline: z.string(), seconds: z.number().min(0.5).max(10) });

// A card whose words and length come from its props (its length is computed: calculateMetadata).
export const Card: React.FC<z.infer<typeof cardSchema>> = ({ headline }) => {
  const frame = useCurrentFrame();
  const { durationInFrames } = useVideoConfig();
  const progress = frame / Math.max(1, durationInFrames - 1);
  return (
    <AbsoluteFill style={{ backgroundColor: COLORS.accent, alignItems: "center", justifyContent: "center", fontFamily: FONT }}>
      <div style={{ color: COLORS.ground, fontSize: 48, textAlign: "center", padding: "0 40px", transform: `scale(${0.9 + progress * 0.1})` }}>{headline}</div>
    </AbsoluteFill>
  );
};
