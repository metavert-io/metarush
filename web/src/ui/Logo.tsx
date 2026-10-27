import { cx } from './primitives';

/**
 * The MetaRush mark: a clapperboard on the amber tile (same drawing as
 * public/favicon.svg). Reads at 16 px and on dark and light backgrounds.
 */
export function Logo({ className, title }: { className?: string; title?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={cx('shrink-0', className)} role={title ? 'img' : undefined} aria-hidden={title ? undefined : true} aria-label={title}>
      <rect width="32" height="32" rx="8" fill="#e0a458" />
      <g fill="#1c1307" stroke="#1c1307" strokeWidth="1.2" strokeLinejoin="round">
        <path className="mr-clapper" d="M6.6 13.3 25 8 23.7 3.6 5.3 8.9Z" />
        <rect x="6.6" y="15" width="18.8" height="11" rx="1.6" />
      </g>
      <g className="mr-clapper" fill="#e0a458">
        <path d="M11.2 12 13.8 11.2 14.4 6.2 11.8 7Z" />
        <path d="M17.4 10.2 20 9.5 20.6 4.5 18 5.2Z" />
      </g>
    </svg>
  );
}
