import React from "react";
import { Audio, Sequence, Series, staticFile } from "remotion";
import { EndCard } from "./scenes/EndCard";
import { Opening } from "./scenes/Opening";

// How long each scene is (frames at 30 a second).
export const FILM_SCENES = { opening: 45, end: 45 } as const;
export const FILM_LENGTH = FILM_SCENES.opening + FILM_SCENES.end;

// A film in two scenes, with music under it and one sound at the end card.
export const Film: React.FC = () => (
  <>
    <Series>
      <Series.Sequence durationInFrames={FILM_SCENES.opening} name="Opening">
        <Opening />
      </Series.Sequence>
      <Series.Sequence durationInFrames={FILM_SCENES.end} name="End card">
        <EndCard />
      </Series.Sequence>
    </Series>
    <Audio src={staticFile("music/bed.wav")} volume={0.3} />
    <Sequence from={FILM_SCENES.opening + 6} durationInFrames={15}>
      <Audio src={staticFile("tone.wav")} volume={0.8} />
    </Sequence>
  </>
);
