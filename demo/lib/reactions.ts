// Canonical reaction keys, mirroring internal/reactions.ValidReactions on
// the backend — the API only accepts these exact keys, never a raw emoji
// glyph (Unicode has multiple byte sequences for the same visible emoji,
// which makes filtering/aggregating on the literal character unreliable).
// This file is the one place the demo maps a key to how it's *displayed*.
export const REACTION_GLYPHS: Record<string, string> = {
  like: "👍",
  dislike: "👎",
  love: "❤️",
  laugh: "😂",
  celebrate: "🎉",
  eyes: "👀",
  rocket: "🚀",
  fire: "🔥",
};

// The quick-react palette shown in the picker — a subset of REACTION_GLYPHS,
// kept smaller than the full set so the popover stays compact.
export const QUICK_REACT_KEYS = ["like", "love", "laugh", "celebrate", "eyes", "rocket"];

export function reactionGlyph(reaction: string): string {
  return REACTION_GLYPHS[reaction] ?? reaction;
}
