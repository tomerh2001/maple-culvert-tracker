-- Durable complete batches. The public integration never accepts raw score IDs.
CREATE TABLE IF NOT EXISTS image_submissions (
 id TEXT PRIMARY KEY,
 principal_id TEXT NOT NULL,
 capture_run_id TEXT NOT NULL,
 collector_id TEXT NOT NULL,
 destination_id TEXT NOT NULL,
 tenant_id TEXT NOT NULL,
 discord_guild_id TEXT NOT NULL,
 actor_id TEXT NOT NULL,
 maple_guild TEXT NOT NULL,
 slot_id TEXT NOT NULL,
 reset_at TIMESTAMPTZ NOT NULL,
 captured_at TIMESTAMPTZ NOT NULL,
 week DATE NOT NULL,
 digest TEXT NOT NULL,
 manifest JSONB NOT NULL,
 state TEXT NOT NULL DEFAULT 'received' CHECK (state IN ('received','committed','rejected')),
 result JSONB NOT NULL DEFAULT '{}',
 error_code TEXT NOT NULL DEFAULT '',
 received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 completed_at TIMESTAMPTZ,
 UNIQUE (principal_id,capture_run_id),
 UNIQUE (tenant_id,maple_guild,collector_id,slot_id,reset_at)
);
CREATE INDEX IF NOT EXISTS image_submissions_pending ON image_submissions(received_at) WHERE state='received';
CREATE INDEX IF NOT EXISTS image_submissions_latest ON image_submissions(tenant_id,maple_guild,week,captured_at) WHERE state='committed';
CREATE TABLE IF NOT EXISTS image_submission_pages (
 submission_id TEXT NOT NULL REFERENCES image_submissions(id) ON DELETE CASCADE,
 ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 20),
 original_png BYTEA NOT NULL CHECK(octet_length(original_png) BETWEEN 1 AND 8388608),
 parsed_names JSONB NOT NULL DEFAULT '[]',
 PRIMARY KEY(submission_id,ordinal)
);
CREATE TABLE IF NOT EXISTS image_submission_deliveries (
 submission_id TEXT NOT NULL REFERENCES image_submissions(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('announcement','archive')),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','delivering','delivered','skipped','outcome_unknown')),
 detail JSONB NOT NULL DEFAULT '{}',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(submission_id,kind)
);

-- Existing manual/legacy score writers and reset-week deletions all pass this
-- table. A transaction-local marker excludes only this API's atomic writes.
CREATE TABLE IF NOT EXISTS culvert_manual_revisions (
 tenant_id TEXT NOT NULL,
 week DATE NOT NULL,
 changed_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(tenant_id,week)
);
-- Existing corrections already have timestamps; deploy must not erase that
-- freshness boundary when the integration is still unconfigured.
INSERT INTO culvert_manual_revisions(tenant_id,week,changed_at)
SELECT c.guild_id,s.culvert_date,MAX(COALESCE(s.updated_at,NOW()))
FROM character_culvert_scores s JOIN characters c ON c.id=s.character_id
GROUP BY c.guild_id,s.culvert_date
ON CONFLICT(tenant_id,week) DO UPDATE
SET changed_at=GREATEST(culvert_manual_revisions.changed_at,EXCLUDED.changed_at);

CREATE OR REPLACE FUNCTION record_culvert_manual_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE tid TEXT; cid BIGINT; target_week DATE;
BEGIN
 IF COALESCE(current_setting('app.culvert_image_origin',true),'') <> '' THEN
  IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
 END IF;
 IF TG_OP='DELETE' THEN cid:=OLD.character_id; target_week:=OLD.culvert_date;
 ELSE cid:=NEW.character_id; target_week:=NEW.culvert_date; END IF;
 SELECT guild_id INTO tid FROM characters WHERE id=cid;
 IF tid IS NOT NULL THEN
  INSERT INTO culvert_manual_revisions(tenant_id,week,changed_at) VALUES(tid,target_week,clock_timestamp())
  ON CONFLICT(tenant_id,week) DO UPDATE SET changed_at=GREATEST(culvert_manual_revisions.changed_at,EXCLUDED.changed_at);
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END $$;
DROP TRIGGER IF EXISTS culvert_manual_revision ON character_culvert_scores;
CREATE TRIGGER culvert_manual_revision AFTER INSERT OR UPDATE OR DELETE ON character_culvert_scores
FOR EACH ROW EXECUTE FUNCTION record_culvert_manual_revision();
