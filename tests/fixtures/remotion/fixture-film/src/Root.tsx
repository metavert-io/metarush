import React from "react";
import { Composition, Folder } from "remotion";
import { Card, cardSchema } from "./Card";
import { Film, FILM_LENGTH } from "./Film";
import { Title } from "./Title";
import { FPS, H, W } from "./theme";

export const RemotionRoot: React.FC = () => {
  return (
    <>
      <Composition id="Title" component={Title} durationInFrames={2 * FPS} fps={FPS} width={W} height={H} />
      <Folder name="Cards">
        <Composition
          id="Card"
          component={Card}
          schema={cardSchema}
          durationInFrames={FPS}
          fps={FPS}
          width={W}
          height={H}
          defaultProps={{ headline: "A card with its own words", seconds: 1.5 }}
          calculateMetadata={({ props }) => ({ durationInFrames: Math.round(props.seconds * FPS) })}
        />
      </Folder>
      <Composition id="Film" component={Film} durationInFrames={FILM_LENGTH} fps={FPS} width={W} height={H} />
    </>
  );
};
