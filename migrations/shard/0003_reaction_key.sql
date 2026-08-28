-- Reactions are canonical string keys (e.g. "like", "rocket"), never a raw
-- emoji glyph — Unicode has multiple byte sequences for the same visible
-- emoji (skin-tone modifiers, variation selectors), which makes filtering/
-- aggregating on the literal character unreliable. The UI maps keys to
-- glyphs purely for display (see internal/reactions.ValidReactions).
ALTER TABLE message_reactions RENAME COLUMN emoji TO reaction;
