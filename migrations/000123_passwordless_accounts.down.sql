-- Down needs every account to have a password again (set one or delete the passwordless ones).
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_password_or_external;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
ALTER TABLE users DROP COLUMN IF EXISTS created_via;
