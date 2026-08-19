-- Track WHEN a score row was last written so /culvert can tell members how
-- fresh the data is ("Scores last updated ..."). Rows that already exist keep
-- NULL: their real write time was never recorded, and deriving one from
-- culvert_date would invent a fact. They get stamped the next time their score
-- actually changes.
ALTER TABLE character_culvert_scores ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
ALTER TABLE character_culvert_scores ALTER COLUMN updated_at SET DEFAULT NOW();
