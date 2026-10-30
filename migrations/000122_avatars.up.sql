-- 0.4.2 1b (owner 2026-10-28): profile pictures. The user picks the source (icon | upload |
-- telegram | line | google); the BE resolves one `avatar` object for every person payload
-- (picked source when available → the icon → initials). Additive only: prod data is real.
CREATE TABLE user_avatars (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    source      VARCHAR(20) NOT NULL CHECK (source IN ('upload', 'telegram', 'line', 'google')),
    image       BYTEA       NOT NULL, -- JPEG, at most 256 px
    version     INTEGER     NOT NULL DEFAULT 1, -- the ?v= of photo_url; +1 on every re-import
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, source)
);

ALTER TABLE users
    ADD COLUMN avatar_settings JSONB NOT NULL
        DEFAULT '{"source": "icon", "colors": [], "border": null}'::jsonb;

-- What the avatar builder needs for one person (shared.AvatarFromSrc): the linked user's icon
-- (else p_icon, the contact's / member's own), their settings and the picked picture when it can
-- be shown (a stored picture of the picked source; for an app source the app is still linked).
-- p_user_id NULL = not an app user (a typed name or an unlinked contact).
CREATE FUNCTION avatar_src(p_user_id UUID, p_icon JSONB) RETURNS JSONB
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT jsonb_build_object(
                    'icon_code', COALESCE(u.icon_code, p_icon),
                    'settings', u.avatar_settings,
                    'photo_id', a.id,
                    'photo_version', a.version)
           FROM users u
           LEFT JOIN user_avatars a
             ON a.user_id = u.id
            AND a.source = u.avatar_settings ->> 'source'
            AND (a.source = 'upload' OR EXISTS (
                    SELECT 1 FROM user_connections c
                     WHERE c.user_id = u.id AND c.provider = a.source))
          WHERE u.id = p_user_id),
        jsonb_build_object('icon_code', p_icon))
$$;
