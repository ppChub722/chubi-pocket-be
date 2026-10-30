-- One-way: the hidden events are gone. Restore the backup taken before the deploy.
DO $$ BEGIN RAISE EXCEPTION 'migration 130 (split bills leave events) cannot be undone: restore the pre-deploy backup'; END $$;
