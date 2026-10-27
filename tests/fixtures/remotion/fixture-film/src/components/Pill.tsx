import React from "react";
import { COLORS, FONT } from "../theme";

// A rounded label in the theme's accent colour.
export const Pill: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <div style={{ padding: "10px 26px", borderRadius: 999, background: COLORS.accent, color: COLORS.ground, fontFamily: FONT, fontSize: 24 }}>{children}</div>
);
