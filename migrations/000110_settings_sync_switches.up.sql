-- 0.4.1-2 S4 / P1 / P2 (BE-3): settings on the server.
--
-- Two auto-sync switches, one value each, read while the server writes another member's book:
--   auto_sync_events — event bills; auto_sync_debts — split bills and debts with a connected contact.
-- Both default ON for new AND existing users (Q-B9). They replace auto_types (auto per notification
-- type) and auto_resolve_own_in_projects (my own event bills into my book, folded into the events
-- switch). Auto is independent of mute: a muted type is applied silently (Q-B10).
ALTER TABLE user_notification_settings
    ADD COLUMN auto_sync_events BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN auto_sync_debts  BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE user_notification_settings
    DROP COLUMN auto_types,
    DROP COLUMN auto_resolve_own_in_projects;

-- P2: registration wrote theme / language defaults no app ever sent. `theme` is replaced by
-- `theme_mode`; a missing language is filled by the app from the device after login.
UPDATE user_preferences SET preferences = preferences - 'theme' - 'language'
WHERE preferences ?| ARRAY['theme', 'language'];
