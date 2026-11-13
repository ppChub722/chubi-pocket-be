-- A savings wallet: the whole balance counts as saved (derived on read, no saving entry per transaction).
ALTER TABLE accounts ADD COLUMN auto_save boolean NOT NULL DEFAULT false;
