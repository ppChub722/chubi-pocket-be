-- One contact name (owner 2026-11-03): no separate nickname. Linked, the switch shows my name
-- for them instead of their app name; it starts off.
ALTER TABLE contacts DROP COLUMN nickname;
ALTER TABLE contacts ALTER COLUMN show_nickname SET DEFAULT false;
UPDATE contacts SET show_nickname = false;
