ALTER TABLE conversations ADD COLUMN welcomed boolean NOT NULL DEFAULT false;
ALTER TABLE jobs ADD COLUMN result jsonb;
ALTER TABLE jobs ADD COLUMN error_code text;
CREATE TABLE step_confirmations (
 id uuid PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 step_id text NOT NULL REFERENCES plan_steps(id),
 expected_version bigint NOT NULL,
 target_state text NOT NULL CHECK(target_state IN ('done','in_progress')),
 expires_at timestamptz NOT NULL,
 result jsonb
);
ALTER TABLE jobs ADD COLUMN payload jsonb;
CREATE UNIQUE INDEX jobs_one_refresh ON jobs(user_id) WHERE kind='refresh' AND state IN ('queued','leased');
ALTER TABLE cards ADD COLUMN origin_message_id bigint REFERENCES messages(id);
CREATE UNIQUE INDEX cards_message_origin ON cards(user_id,type,origin_message_id);
ALTER TABLE messages ADD COLUMN metadata jsonb;
CREATE TABLE dialogue_state(user_id text PRIMARY KEY REFERENCES users(id),payload jsonb NOT NULL);
ALTER TABLE events ADD COLUMN role text;
ALTER TABLE events ADD COLUMN team text;
ALTER TABLE events ADD COLUMN period integer;
ALTER TABLE events ADD COLUMN session_id text;
ALTER TABLE events ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}';
CREATE FUNCTION event_identity_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT role,team_id,GREATEST(1,1+floor((((NEW.created_at AT TIME ZONE current_setting('TimeZone'))::date)-start_date)/7.0)::int)
 INTO NEW.role,NEW.team,NEW.period FROM users WHERE id=NEW.user_id;
 NEW.session_id:=COALESCE(NEW.session_id,'conversation:'||NEW.user_id);
 RETURN NEW;
END;
$$;
CREATE TRIGGER event_identity BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION event_identity_snapshot();
