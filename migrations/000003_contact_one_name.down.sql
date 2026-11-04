ALTER TABLE contacts ADD COLUMN nickname character varying(100);
ALTER TABLE contacts ALTER COLUMN show_nickname SET DEFAULT true;
