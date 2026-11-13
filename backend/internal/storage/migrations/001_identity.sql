CREATE TABLE users (
 id text PRIMARY KEY,
 name text NOT NULL,
 role text NOT NULL CHECK (role IN ('intern','buddy','manager','hr')),
 team_id text,
 start_date date NOT NULL,
 buddy_id text REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED,
 manager_id text REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED,
 curator_id text REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED,
 policy_version bigint NOT NULL DEFAULT 1 CHECK (policy_version > 0),
 enabled boolean NOT NULL DEFAULT true
);
CREATE TABLE sessions (
 token_hash text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE plan_steps (
 id text PRIMARY KEY,
 intern_id text NOT NULL REFERENCES users(id),
 code text NOT NULL,
 title text NOT NULL,
 period integer NOT NULL CHECK (period > 0),
 owner_id text NOT NULL REFERENCES users(id),
 closure text NOT NULL CHECK (closure IN ('manual','service')),
 status text NOT NULL CHECK (status IN ('not_started','in_progress','waiting','attention','done')),
 dependencies text[] NOT NULL DEFAULT '{}',
 source text NOT NULL,
 updated_at timestamptz NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 UNIQUE(intern_id,code)
);
CREATE TABLE step_assignments (
 step_id text PRIMARY KEY REFERENCES plan_steps(id),
 owner_id text NOT NULL REFERENCES users(id)
);
CREATE INDEX step_assignments_owner ON step_assignments(owner_id);
CREATE TABLE documents (
 id text PRIMARY KEY,
 title text NOT NULL,
 team_id text,
 roles text[] NOT NULL,
 owner_id text NOT NULL REFERENCES users(id),
 url text NOT NULL,
 stale boolean NOT NULL DEFAULT false,
 fragments jsonb NOT NULL,
 demo boolean NOT NULL DEFAULT true
);
CREATE TABLE resources (
 id text PRIMARY KEY,
 name text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('team','common','task','closed','hidden')),
 levels text[] NOT NULL,
 default_level text NOT NULL,
 approver_id text NOT NULL,
 recommended_teams text[] NOT NULL,
 synonyms text[] NOT NULL,
 policy_version bigint NOT NULL DEFAULT 1
);
CREATE FUNCTION advance_user_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.role,NEW.team_id,NEW.start_date,NEW.buddy_id,NEW.manager_id,NEW.curator_id,NEW.enabled)
    IS DISTINCT FROM ROW(OLD.role,OLD.team_id,OLD.start_date,OLD.buddy_id,OLD.manager_id,OLD.curator_id,OLD.enabled) THEN
  NEW.policy_version := OLD.policy_version + 1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER users_policy_change BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION advance_user_policy();
CREATE FUNCTION advance_catalog_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE users SET policy_version=policy_version+1;
 RETURN NEW;
END $$;
CREATE TRIGGER resources_policy_change AFTER UPDATE OR DELETE ON resources FOR EACH STATEMENT EXECUTE FUNCTION advance_catalog_policy();
