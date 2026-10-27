import { loadFont } from "@remotion/google-fonts/Anton";

const anton = loadFont("normal", { weights: ["400"], subsets: ["latin"] });

export const FONT = anton.fontFamily;
export const COLORS = { ground: "#14141C", ink: "#F2F2F7", accent: "#E0A458" } as const;
export const W = 640;
export const H = 360;
export const FPS = 30;
