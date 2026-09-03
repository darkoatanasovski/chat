-- Translation cache: a genuinely separate resource per (message, target
-- language) rather than something denormalized onto the message row the
-- way reactions/pins are — a message can be translated into arbitrarily
-- many languages, so unlike a pin flag there's no single fixed-width value
-- to add as a column. Same "separate entity, own table" shape as polls
-- (migrations/shard/0007_polls.sql), keyed the same way message_reactions
-- is: (channel_id, message_id, ...) referencing the unique index
-- idx_messages_channel_message already created by
-- migrations/shard/0002_reactions.sql.
--
-- This exists purely to avoid re-paying the configured translation
-- provider (internal/translations) for a message/language pair that's
-- already been translated — it is not itself the usage/billing record
-- (see migrations/control/0014_translation_usage.sql for that).

CREATE TABLE message_translations (
    channel_id       UUID NOT NULL,
    message_id       UUID NOT NULL,
    target_lang      TEXT NOT NULL,
    -- source_lang is Azure's own detected source language when the
    -- request didn't pin one explicitly (internal/translations.Client
    -- always returns detection results regardless).
    source_lang      TEXT NOT NULL,
    translated_body  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (channel_id, message_id, target_lang),
    FOREIGN KEY (channel_id, message_id) REFERENCES messages (channel_id, message_id)
);
