// A chat window with a message thread that scrolls the way a real chat
// does: each new bubble arrives at the bottom and the whole log slides up
// to make room, until the oldest messages scroll out of view past the
// header — orbited by three small nodes on dashed arcs, the platform's own
// story (multi-region delivery) told as one small line-art scene, in the
// same monoline-illustration spirit as the landing page's reference
// composition, redrawn for a dark theme: muted strokes for structure, the
// product's accent green for the one thing that should draw the eye (the
// message just sent, and the region nodes it just reached). One 9s
// keyframe drives the whole message log's position (see globals.css);
// each bubble's own arrival is a quick fade+pop staggered with
// animation-delay, so the whole scene loops in sync without any JS.
export function HeroIllustration() {
  return (
    <svg viewBox="0 0 600 460" className="h-auto w-full max-w-lg" role="img" aria-label="A chat window with messages scrolling by as they arrive">
      <g className="stroke-border-soft" strokeWidth="1.5" fill="none" strokeDasharray="3 6">
        <path d="M150 120 Q60 90 44 150" />
        <path d="M420 100 Q520 90 546 160" />
        <path d="M430 300 Q520 330 508 380" />
      </g>

      <g>
        <circle cx="44" cy="158" r="18" className="chat-ping-ring fill-none stroke-accent" strokeWidth="2" style={{ animationDelay: "3.6s" }} />
        <circle cx="44" cy="158" r="10" className="fill-surface stroke-accent" strokeWidth="2" />
        <circle cx="546" cy="168" r="10" className="fill-surface stroke-text-faint" strokeWidth="2" />
        <circle cx="506" cy="388" r="10" className="fill-surface stroke-text-faint" strokeWidth="2" />
        <circle cx="44" cy="158" r="3.5" className="fill-accent">
          <animate attributeName="opacity" values="1;0.35;1" dur="2.4s" repeatCount="indefinite" />
        </circle>
      </g>

      <rect x="120" y="46" width="360" height="300" rx="18" className="fill-surface stroke-border" strokeWidth="2" />
      <line x1="120" y1="94" x2="480" y2="94" className="stroke-border" strokeWidth="2" />
      <circle cx="147" cy="70" r="5" className="fill-danger" opacity="0.55" />
      <circle cx="167" cy="70" r="5" className="fill-warning" opacity="0.55" />
      <circle cx="187" cy="70" r="5" className="fill-success" opacity="0.55" />

      {/* Clips the message log to the window's body, below the header —
          this is what makes older bubbles disappear as the log scrolls
          past the top, instead of floating over the title bar. */}
      <defs>
        <clipPath id="heroChatClip">
          <rect x="120" y="94" width="360" height="244" />
        </clipPath>
      </defs>

      <g clipPath="url(#heroChatClip)">
        <g className="chat-scroll">
          <rect x="148" y="0" width="150" height="32" rx="11" className="chat-bubble fill-surface-2" style={{ animationDelay: "0s" }} />
          <rect x="148" y="64" width="110" height="32" rx="11" className="chat-bubble fill-surface-2" style={{ animationDelay: "1.8s" }} />
          <rect x="298" y="128" width="134" height="32" rx="11" className="chat-bubble fill-accent" opacity="0.9" style={{ animationDelay: "3.6s" }} />
          <rect x="148" y="192" width="90" height="32" rx="11" className="chat-bubble fill-surface-2" style={{ animationDelay: "5.4s" }} />

          <g className="chat-typing-wrap fill-text-faint">
            <circle cx="160" cy="272" r="4" className="chat-typing-dot" style={{ animationDelay: "0s" }} />
            <circle cx="176" cy="272" r="4" className="chat-typing-dot" style={{ animationDelay: "0.15s" }} />
            <circle cx="192" cy="272" r="4" className="chat-typing-dot" style={{ animationDelay: "0.3s" }} />
          </g>
        </g>
      </g>
    </svg>
  );
}
