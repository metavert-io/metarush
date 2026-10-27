import { useEffect, useState } from 'react';
import { cx } from '../../../ui/primitives';
import type { Mic } from './capture';

/** The microphone level, with plain words when it is too quiet or too loud. */
export function LevelMeter({ mic }: { mic: Mic }) {
  const [lv, setLv] = useState({ rms: 0, hold: 0 });
  useEffect(() => {
    let raf = 0;
    let last = 0;
    const tick = (t: number) => {
      if (t - last > 50) {
        last = t;
        const l = mic.level();
        setLv({ rms: l.rms, hold: l.hold });
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [mic]);
  const db = (v: number) => (v > 0 ? 20 * Math.log10(v) : -100);
  const pos = (v: number) => Math.max(0, Math.min(100, ((db(v) + 60) / 60) * 100));
  const peakDb = db(lv.hold);
  const words = peakDb > -1 ? 'Too loud' : peakDb < -50 ? 'Silent: check the microphone' : peakDb < -30 ? 'Quiet: speak up or move closer' : 'Good';
  return (
    <div className="flex items-center gap-2" role="meter" aria-label="Microphone level" aria-valuemin={-60} aria-valuemax={0} aria-valuenow={Math.round(Math.max(-60, db(lv.rms)))} aria-valuetext={words}>
      <div className="relative h-2 w-[120px] overflow-hidden rounded-full bg-white/10">
        <div className="absolute inset-y-0 left-0 rounded-full bg-[linear-gradient(to_right,#92c79c_0%,#92c79c_70%,#e0a458_85%,#ef8d80_100%)]" style={{ width: `${pos(lv.rms)}%` }} />
        <div className="absolute inset-y-0 w-[2px] bg-white" style={{ left: `${pos(lv.hold)}%` }} />
      </div>
      <span className={cx('w-[190px] text-[12px]', words === 'Good' ? 'text-[#92c79c]' : words === 'Too loud' ? 'text-[#ef8d80]' : 'text-[#e8b169]')}>{words}</span>
    </div>
  );
}
