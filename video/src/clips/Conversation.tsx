import type { PlanClip, PlanRect, PlanShot, PlanTile, RenderPlan } from '@metarush/schema';
import React, { useMemo } from 'react';
import { AbsoluteFill, OffthreadVideo, Sequence, interpolate, useCurrentFrame, useVideoConfig } from 'remotion';
import { exactTrim } from '../audio/AudioLayer';
import { resolveBrand, rgba, type ResolvedBrand } from '../theme/brand';

/**
 * A conversation clip's picture (D99, D102): its shots as Go resolved them —
 * one person, a layout, a text card or b-roll — in clip-relative frames, each
 * tile's rectangle and where the recording's picture sits inside it already
 * computed (faces at one size and eye line). The pictures play muted: the
 * sound is every member's own track in the plan's audio. A shot with
 * `dissolveFrames` fades in over the one before, which keeps playing under it.
 */
export const ConversationPicture: React.FC<{ clip: PlanClip; plan: RenderPlan }> = ({ clip, plan }) => {
  const conv = clip.conversation;
  const { fps } = useVideoConfig();
  const brand = useMemo(() => resolveBrand(plan.brand), [plan.brand]);
  if (!conv) return null;
  const shots = conv.shots ?? [];
  return (
    <AbsoluteFill style={{ backgroundColor: plan.background || '#000' }}>
      {shots.map((shot, i) => {
        const next = shots[i + 1];
        const hold = Math.max(0, next?.dissolveFrames ?? 0);
        const dur = shot.durationInFrames + hold;
        if (!(dur > 0)) return null;
        return (
          <Sequence
            key={`shot-${i}`}
            from={shot.from}
            durationInFrames={dur}
            premountFor={Math.max(1, Math.round(fps))}
            name={`shot ${i + 1} ${shot.kind}${shot.layout ? ` ${shot.layout}` : ''}`}
          >
            <ShotView shot={shot} plan={plan} brand={brand} clipID={clip.id} index={i} />
          </Sequence>
        );
      })}
    </AbsoluteFill>
  );
};

const ShotView: React.FC<{ shot: PlanShot; plan: RenderPlan; brand: ResolvedBrand; clipID: string; index: number }> = ({ shot, plan, brand, clipID, index }) => {
  const frame = useCurrentFrame();
  const d = Math.max(0, shot.dissolveFrames ?? 0);
  const opacity = d > 0 ? interpolate(frame, [0, d], [0, 1], { extrapolateLeft: 'clamp', extrapolateRight: 'clamp' }) : 1;
  return (
    <AbsoluteFill style={{ opacity, backgroundColor: shot.backdrop || undefined }}>
      {(shot.tiles ?? []).map((tile, k) => (
        <Tile key={`t-${k}`} tile={tile} src={plan.sources?.[tile.assetId]} brand={brand} filterID={`conv-${clipID}-${index}-${k}`} />
      ))}
      {shot.card ? <Card text={shot.card.text} brand={brand} softBehind={(shot.tiles ?? []).some((t) => t.soft)} /> : null}
    </AbsoluteFill>
  );
};

/** One picture in its rectangle; the recording placed (and cropped) as Go said. */
const Tile: React.FC<{ tile: PlanTile; src: string | undefined; brand: ResolvedBrand; filterID: string }> = ({ tile, src, brand, filterID }) => {
  const r = tile.rect;
  const win = tile.window;
  const v = tile.video;
  const rate = Number.isFinite(tile.playbackRate) && tile.playbackRate > 0 ? tile.playbackRate : 1;
  const color = tile.color && tile.color.length === 20 ? tile.color : null;
  // The exact place in the recording (D119): the picture follows the same time as the person's sound.
  const trim = exactTrim(tile);
  const filters: string[] = [];
  if (color) filters.push(`url(#${filterID})`);
  if (tile.soft) filters.push('blur(28px) brightness(0.55)');
  const video = src ? (
    <OffthreadVideo
      src={src}
      muted
      trimBefore={trim > 0 ? trim : undefined}
      playbackRate={rate}
      pauseWhenBuffering
      style={{
        position: 'absolute',
        left: (v.x - (win?.x ?? 0)) + (tile.soft ? -v.w * 0.05 : 0),
        top: (v.y - (win?.y ?? 0)) + (tile.soft ? -v.h * 0.05 : 0),
        width: v.w * (tile.soft ? 1.1 : 1),
        height: v.h * (tile.soft ? 1.1 : 1),
        maxWidth: 'none',
        objectFit: 'fill',
        filter: filters.length ? filters.join(' ') : undefined,
      }}
    />
  ) : null;
  const delayed = video && (tile.delay ?? 0) > 0 ? <Sequence from={tile.delay} layout="none">{video}</Sequence> : video;
  return (
    <div
      style={{
        position: 'absolute',
        left: r.x,
        top: r.y,
        width: r.w,
        height: r.h,
        overflow: 'hidden',
        borderRadius: tile.radius ?? 0,
        backgroundColor: '#000',
        boxShadow: (tile.radius ?? 0) > 0 ? '0 6px 24px rgba(0,0,0,0.45)' : undefined,
      }}
    >
      {color ? (
        <svg width={0} height={0} style={{ position: 'absolute' }} aria-hidden>
          <filter id={filterID} colorInterpolationFilters="sRGB">
            <feColorMatrix type="matrix" values={color.join(' ')} />
          </filter>
        </svg>
      ) : null}
      {win ? (
        <div style={{ position: 'absolute', left: win.x, top: win.y, width: win.w, height: win.h, overflow: 'hidden' }}>{delayed}</div>
      ) : (
        delayed
      )}
      {tile.border && tile.border.width > 0 ? (
        // A line around the picture in one of the brand's colours (D265), drawn over it.
        <div
          style={{
            position: 'absolute',
            inset: 0,
            borderRadius: tile.radius ?? 0,
            border: `${tile.border.width}px solid ${tile.border.color}`,
            boxSizing: 'border-box',
            pointerEvents: 'none',
          }}
        />
      ) : null}
      {tile.tag?.cover ? (
        <CoverTag name={tile.tag.name} cover={tile.tag.cover} brand={brand} tileW={r.w} tileH={r.h} />
      ) : tile.tag ? (
        <NameTag name={tile.tag.name} title={tile.tag.title} brand={brand} tileH={r.h} tileW={r.w} box={tile.tag.box} />
      ) : null}
    </div>
  );
};

/**
 * A person's name (and their role) at the bottom of their tile: the
 * bottom-left corner, or — in a round inset (D265) — centred at its foot in
 * the box Go placed inside the circle.
 */
const NameTag: React.FC<{ name: string; title?: string; brand: ResolvedBrand; tileH: number; tileW: number; box?: PlanRect }> = ({ name, title, brand, tileH, tileW, box }) => {
  const size = Math.max(14, Math.round(Math.min(tileH * 0.042, tileW * 0.05)));
  const pad = Math.round(size * 0.55);
  const place: React.CSSProperties = box
    ? { left: box.x + box.w / 2, top: box.y + box.h, transform: 'translate(-50%, -100%)', maxWidth: box.w, boxSizing: 'border-box', textAlign: 'center' }
    : { left: pad, bottom: pad, maxWidth: tileW - 2 * pad };
  return (
    <div
      style={{
        position: 'absolute',
        ...place,
        padding: `${Math.round(size * 0.3)}px ${Math.round(size * 0.6)}px`,
        borderRadius: Math.round(size * 0.35),
        backgroundColor: rgba('#000000', 0.55),
        color: brand.text,
        fontFamily: brand.fontFamily,
        lineHeight: 1.15,
        borderLeft: `${Math.max(2, Math.round(size * 0.14))}px solid ${brand.accent}`,
      }}
    >
      <div style={{ fontSize: size, fontWeight: 600, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</div>
      {title ? (
        <div style={{ fontSize: Math.round(size * 0.72), opacity: 0.85, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{title}</div>
      ) : null}
    </div>
  );
};

/**
 * A tile's name tag over the name a call app printed in its recording (D182):
 * an opaque box in the brand's colours covering that area, with the person's
 * name, so a layout's tiles show the cut's own names (one name bar can't
 * cover several tiles).
 */
const CoverTag: React.FC<{ name: string; cover: { x: number; y: number; w: number; h: number }; brand: ResolvedBrand; tileW: number; tileH: number }> = ({
  name,
  cover,
  brand,
  tileW,
  tileH,
}) => {
  const grow = Math.max(4, Math.round(cover.h * 0.25));
  const x = Math.max(0, cover.x - grow);
  const y = Math.max(0, cover.y - grow);
  const w = Math.min(tileW - x, cover.w + 2 * grow);
  const h = Math.min(tileH - y, cover.h + 2 * grow);
  const size = Math.max(12, Math.round(Math.min(h * 0.55, tileH * 0.05)));
  return (
    <div
      data-cover-tag
      style={{
        position: 'absolute',
        left: x,
        top: y,
        width: Math.max(w, Math.min(tileW - x, name.length * size * 0.62 + size * 1.4)),
        height: h,
        display: 'flex',
        alignItems: 'center',
        padding: `0 ${Math.round(size * 0.6)}px`,
        boxSizing: 'border-box',
        backgroundColor: brand.background,
        color: brand.text,
        fontFamily: brand.fontFamily,
        fontSize: size,
        fontWeight: 600,
        whiteSpace: 'nowrap',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        borderLeft: `${Math.max(2, Math.round(size * 0.14))}px solid ${brand.accent}`,
      }}
    >
      {name}
    </div>
  );
};

/** A text card (an off-camera interviewer's question), over a softened picture or the brand's background. */
const Card: React.FC<{ text: string; brand: ResolvedBrand; softBehind: boolean }> = ({ text, brand, softBehind }) => {
  const { width, height } = useVideoConfig();
  const frame = useCurrentFrame();
  const size = Math.round(Math.min(width, height) * (text.length > 80 ? 0.05 : 0.065));
  const rise = interpolate(frame, [0, 10], [12, 0], { extrapolateRight: 'clamp' });
  const fade = interpolate(frame, [0, 8], [0, 1], { extrapolateRight: 'clamp' });
  return (
    <AbsoluteFill style={{ alignItems: 'center', justifyContent: 'center', backgroundColor: softBehind ? rgba(brand.background, 0.35) : brand.background }}>
      <div
        style={{
          maxWidth: '78%',
          color: brand.text,
          fontFamily: brand.fontFamily,
          fontSize: size,
          fontWeight: 600,
          lineHeight: 1.25,
          textAlign: 'center',
          opacity: fade,
          transform: `translateY(${rise}px)`,
          textShadow: softBehind ? '0 2px 18px rgba(0,0,0,0.5)' : undefined,
        }}
      >
        <div style={{ width: size * 1.6, height: Math.max(3, Math.round(size * 0.09)), backgroundColor: brand.accent, margin: `0 auto ${Math.round(size * 0.6)}px` }} />
        {text ? `“${text}”` : null}
      </div>
    </AbsoluteFill>
  );
};
