-- Irreversible: the old split / event bills were dropped. Restore the dump taken before 000002.
DO $$ BEGIN RAISE EXCEPTION 'migration 000002_paper is irreversible: restore the pre-000002 dump'; END $$;
