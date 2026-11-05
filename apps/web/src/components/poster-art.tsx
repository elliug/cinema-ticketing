// Poster art for the listings board (Marquesina).
//
// No image is fetched: the web app makes no request for a picture, so a poster is a small
// inline drawing whose plate and shape are derived from the film title. The derivation is
// deterministic — the same film always draws the same poster — and it is *decoration*: the
// drawing is `aria-hidden` because every fact it might suggest (a genre, a mood) is either
// unknown to us or already printed next to it.
//
// The colours are tokens, never literals: `components.test.ts` refuses a hex value in a
// component, and a poster that carried its own palette would survive a palette change
// unnoticed.
const TONES = [
  { plate: "var(--color-warning-bg)", ink: "var(--color-accent)" },
  { plate: "var(--color-info-bg)", ink: "var(--color-info-fg)" },
  { plate: "var(--color-success-bg)", ink: "var(--color-success-fg)" },
  { plate: "var(--color-danger-bg)", ink: "var(--color-danger-fg)" },
] as const;

const SHAPES = ["sun", "letter", "cone", "bars"] as const;

type Shape = (typeof SHAPES)[number];

/** A small, stable fingerprint of the title: same film, same poster, on every render. */
function fingerprint(title: string): number {
  let hash = 0;
  for (let index = 0; index < title.length; index += 1) {
    hash = (hash * 31 + title.charCodeAt(index)) | 0;
  }
  return Math.abs(hash);
}

function Drawing({ shape, ink }: { shape: Shape; ink: string }) {
  switch (shape) {
    case "sun":
      return (
        <>
          <circle cx="50" cy="64" r="24" fill={ink} />
          <g stroke={ink} strokeOpacity="0.5" strokeWidth="1.5">
            <path d="M50 24v10M50 94v10M10 64h10M80 64h10M22 36l7 7M71 85l7 7M22 92l7-7M71 43l7-7" />
          </g>
        </>
      );
    case "letter":
      return (
        <>
          <rect x="24" y="42" width="52" height="38" fill="none" stroke={ink} strokeWidth="2" />
          <path d="M24 42l26 22 26-22" fill="none" stroke={ink} strokeWidth="2" />
          <g stroke={ink} strokeOpacity="0.4" strokeWidth="1">
            <path d="M16 96h68M16 106h68M16 116h68" />
          </g>
        </>
      );
    case "cone":
      return (
        <>
          <path d="M50 30l30 74H20z" fill={ink} fillOpacity="0.5" />
          <circle cx="50" cy="30" r="6" fill={ink} />
          <g stroke={ink} strokeOpacity="0.35" strokeWidth="1">
            <path d="M18 112h64M26 122h48" />
          </g>
        </>
      );
    case "bars":
      return (
        <g fill={ink} fillOpacity="0.85">
          <rect x="22" y="52" width="7" height="44" rx="3" />
          <rect x="38" y="36" width="7" height="60" rx="3" />
          <rect x="54" y="60" width="7" height="36" rx="3" />
          <rect x="70" y="28" width="7" height="68" rx="3" />
        </g>
      );
  }
}

export interface PosterArtProps {
  /** The film title the drawing is derived from. */
  title: string;
  className?: string;
}

export function PosterArt({ title, className }: PosterArtProps) {
  const print = fingerprint(title);
  const tone = TONES[print % TONES.length];
  const shape = SHAPES[Math.floor(print / 4) % SHAPES.length];

  return (
    <svg
      className={className === undefined ? "poster" : `poster ${className}`}
      viewBox="0 0 100 150"
      aria-hidden="true"
      focusable="false"
    >
      <rect width="100" height="150" fill={tone.plate} />
      <Drawing shape={shape} ink={tone.ink} />
      <line x1="14" y1="132" x2="86" y2="132" stroke={tone.ink} strokeOpacity="0.35" />
    </svg>
  );
}
