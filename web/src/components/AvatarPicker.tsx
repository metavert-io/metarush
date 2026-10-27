import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type WheelEvent } from 'react';
import { Camera, MagnifyingGlassMinus, MagnifyingGlassPlus, Trash } from '@phosphor-icons/react';
import { useQueryClient } from '@tanstack/react-query';
import { call, toApiError } from '../api/client';
import { Avatar } from '../ui/Avatar';
import { useDialog } from '../ui/Dialog';
import { useToast } from '../ui/Toast';
import { Button } from '../ui/primitives';

/*
 * Your picture (D184): choose a PNG, JPEG or WebP (up to 5 MB), pick the
 * square to keep in a styled dialog (drag to move, zoom with the slider or the
 * wheel), and the square is drawn to a canvas and sent as a PNG through
 * account.avatar. The server checks it again, takes the middle square and
 * scales it to 256 pixels, so this step only chooses the part to keep.
 */

const TYPES = ['image/png', 'image/jpeg', 'image/webp'];
const MAX_BYTES = 5 << 20;
const OUT = 512;
const MAX_ZOOM = 4;

/** Plain words for a file that can't be a picture (the server says the same). */
export const NOT_A_PICTURE = "That isn't a picture MetaRush can use. Choose a PNG, JPEG or WebP file.";
const TOO_BIG = 'That picture is larger than 5 MB; choose a smaller one.';
const UNREADABLE = "MetaRush couldn't read that picture. Try another file.";
const TOO_SMALL = 'Choose a picture at least 32 pixels on each side.';

/** Queries that show pictures, refreshed after a change. */
const AVATAR_QUERIES = ['session', 'users.list', 'auth.status'];

function isPictureFile(f: File): boolean {
  if (TYPES.includes(f.type)) return true;
  return f.type === '' && /\.(png|jpe?g|webp)$/i.test(f.name);
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(UNREADABLE));
    img.src = url;
  });
}

/** Your picture, with Choose a picture and Remove picture (Settings → People → Your account). */
export function AvatarControl({ name, avatar }: { name: string; avatar?: string }) {
  const dialog = useDialog();
  const toast = useToast();
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState<'' | 'save' | 'remove'>('');
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    for (const k of AVATAR_QUERIES) void qc.invalidateQueries({ queryKey: [k] });
  };

  const choose = async (f: File) => {
    setError(null);
    if (!isPictureFile(f)) {
      setError(NOT_A_PICTURE);
      return;
    }
    if (f.size > MAX_BYTES) {
      setError(TOO_BIG);
      return;
    }
    const url = URL.createObjectURL(f);
    try {
      let img: HTMLImageElement;
      try {
        img = await loadImage(url);
      } catch {
        setError(UNREADABLE);
        return;
      }
      if (img.naturalWidth < 32 || img.naturalHeight < 32) {
        setError(TOO_SMALL);
        return;
      }
      const data = await dialog.open<string>((close) => <CropPicture img={img} close={close} />, { title: 'Choose the part to show', size: 'sm' });
      if (!data) return;
      setBusy('save');
      try {
        const out = await call('account.avatar', { data });
        toast({ tone: 'success', title: out.summary });
        refresh();
      } catch (e) {
        const err = toApiError(e);
        setError([err.message, err.hint].filter(Boolean).join(' '));
      } finally {
        setBusy('');
      }
    } finally {
      URL.revokeObjectURL(url);
    }
  };

  const remove = async () => {
    setError(null);
    setBusy('remove');
    try {
      const out = await call('account.avatar', { remove: true });
      toast({ tone: 'success', title: out.summary });
      refresh();
    } catch (e) {
      const err = toApiError(e);
      setError([err.message, err.hint].filter(Boolean).join(' '));
    } finally {
      setBusy('');
    }
  };

  return (
    <div className="mt-3" data-avatar-control>
      <div className="flex flex-wrap items-center gap-3">
        <Avatar src={avatar} name={name} size={56} marker="your-avatar" />
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <Button size="sm" variant="secondary" icon={<Camera size={13} />} loading={busy === 'save'} disabled={!!busy} onClick={() => input.current?.click()} data-avatar-choose>
            Choose a picture
          </Button>
          {avatar ? (
            <Button size="sm" variant="ghost" icon={<Trash size={13} />} loading={busy === 'remove'} disabled={!!busy} onClick={() => void remove()} data-avatar-remove>
              Remove picture
            </Button>
          ) : null}
        </div>
      </div>
      <p className="mt-2 text-[12px] text-subtle">A PNG, JPEG or WebP, up to 5 MB. Others see it next to your name; without one, your initials show.</p>
      {error ? (
        <p role="alert" className="mt-2 rounded-[10px] bg-danger-soft px-3 py-2 text-[12.5px] leading-relaxed text-danger" data-avatar-error>
          {error}
        </p>
      ) : null}
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/webp"
        className="hidden"
        aria-hidden
        tabIndex={-1}
        data-avatar-input
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = '';
          if (f) void choose(f);
        }}
      />
    </div>
  );
}

type View = { x: number; y: number; z: number };

/** Drag and zoom a picture inside a square; returns the square as a base64 PNG. */
function CropPicture({ img, close }: { img: HTMLImageElement; close: (v?: string) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const [side, setSide] = useState(0);
  const [view, setView] = useState<View>({ x: 0, y: 0, z: 1 });
  const drag = useRef<{ id: number; x: number; y: number } | null>(null);
  const w = img.naturalWidth;
  const h = img.naturalHeight;
  const base = side ? side / Math.min(w, h) : 1;

  const clamp = useCallback(
    (v: View): View => {
      const z = Math.min(MAX_ZOOM, Math.max(1, v.z));
      const s = base * z;
      return { z, x: Math.min(0, Math.max(side - w * s, v.x)), y: Math.min(0, Math.max(side - h * s, v.y)) };
    },
    [base, side, w, h],
  );

  // The square's size follows the dialog's width; the picture starts centred, filling it.
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    // clientWidth: the layout size (the dialog's opening animation scales what is drawn).
    const n = el.clientWidth;
    setSide(n);
    const s = n / Math.min(w, h);
    setView({ z: 1, x: (n - w * s) / 2, y: (n - h * s) / 2 });
  }, [w, h]);

  const zoomTo = (z: number) =>
    setView((v) => {
      const s = base * v.z;
      const cx = (side / 2 - v.x) / s;
      const cy = (side / 2 - v.y) / s;
      const nz = Math.min(MAX_ZOOM, Math.max(1, z));
      const ns = base * nz;
      return clamp({ z: nz, x: side / 2 - cx * ns, y: side / 2 - cy * ns });
    });

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId);
    drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY };
  };
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    drag.current = { id: d.id, x: e.clientX, y: e.clientY };
    setView((v) => clamp({ ...v, x: v.x + dx, y: v.y + dy }));
  };
  const onPointerUp = (e: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.id === e.pointerId) drag.current = null;
  };
  const onWheel = (e: WheelEvent<HTMLDivElement>) => {
    zoomTo(view.z * Math.exp(-e.deltaY * 0.0015));
  };
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 40 : 10;
    const moves: Record<string, [number, number]> = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] };
    const m = moves[e.key];
    if (m) {
      e.preventDefault();
      setView((v) => clamp({ ...v, x: v.x + m[0], y: v.y + m[1] }));
    } else if (e.key === '+' || e.key === '=') {
      e.preventDefault();
      zoomTo(view.z * 1.15);
    } else if (e.key === '-') {
      e.preventDefault();
      zoomTo(view.z / 1.15);
    }
  };

  // Wheel zoom without scrolling the page behind the dialog.
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const stop = (e: globalThis.WheelEvent) => e.preventDefault();
    el.addEventListener('wheel', stop, { passive: false });
    return () => el.removeEventListener('wheel', stop);
  }, []);

  const save = () => {
    const s = base * view.z;
    const sx = -view.x / s;
    const sy = -view.y / s;
    const ss = side / s;
    const canvas = document.createElement('canvas');
    canvas.width = OUT;
    canvas.height = OUT;
    const ctx = canvas.getContext('2d');
    if (!ctx) return close();
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(img, sx, sy, ss, ss, 0, 0, OUT, OUT);
    const url = canvas.toDataURL('image/png');
    close(url.slice(url.indexOf(',') + 1));
  };

  const s = base * view.z;
  return (
    <div data-avatar-crop>
      <p className="mb-3 text-[13px] leading-relaxed text-muted">Drag the picture to place it, and zoom to show more or less. The round part is what others see.</p>
      <div
        ref={box}
        role="img"
        aria-label="Your picture, to place and zoom"
        tabIndex={0}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onWheel={onWheel}
        onKeyDown={onKey}
        className="relative mx-auto aspect-square w-full max-w-[300px] cursor-grab touch-none select-none overflow-hidden rounded-[14px] bg-inset outline-none hairline focus-visible:ring-2 focus-visible:ring-accent active:cursor-grabbing"
      >
        {side ? (
          <img
            src={img.src}
            alt=""
            draggable={false}
            className="pointer-events-none absolute left-0 top-0 max-w-none"
            style={{ width: w * s, height: h * s, transform: `translate(${view.x}px, ${view.y}px)` }}
          />
        ) : null}
        <div className="pointer-events-none absolute inset-0 rounded-full shadow-[0_0_0_9999px_rgba(0,0,0,0.45)] ring-1 ring-white/60" aria-hidden />
      </div>
      <label className="mx-auto mt-4 flex max-w-[300px] items-center gap-2.5 text-[12.5px] text-muted">
        <MagnifyingGlassMinus size={15} className="shrink-0 text-subtle" aria-hidden />
        <span className="sr-only">Zoom</span>
        <input
          type="range"
          min={1}
          max={MAX_ZOOM}
          step={0.01}
          value={view.z}
          onChange={(e) => zoomTo(Number(e.target.value))}
          className="min-w-0 flex-1 accent-[var(--accent)]"
          data-avatar-zoom
        />
        <MagnifyingGlassPlus size={15} className="shrink-0 text-subtle" aria-hidden />
      </label>
      <div className="mt-6 flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={() => close()}>
          Cancel
        </Button>
        <Button variant="primary" disabled={!side} onClick={save} data-avatar-crop-save data-autofocus>
          Use this picture
        </Button>
      </div>
    </div>
  );
}
