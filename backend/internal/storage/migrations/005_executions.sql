CREATE TABLE execution_runs (
 id uuid PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 scenario text NOT NULL CHECK(scenario IN ('orientation','access','time_off','transfer')),
 state text NOT NULL CHECK(state IN ('queued','running','waiting_user','reconciling','manual_review','completed','rejected','failed','cancelled')),
 current_node_id text NOT NULL DEFAULT '',
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 last_sequence bigint NOT NULL DEFAULT 0 CHECK(last_sequence>=0),
 started_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 result_code text NOT NULL DEFAULT '',
 detail_complete boolean NOT NULL DEFAULT true,
 UNIQUE(id,user_id)
);
CREATE INDEX execution_owner_history ON execution_runs(user_id,started_at DESC,id DESC);
CREATE INDEX execution_owner_active ON execution_runs(user_id,started_at DESC,id DESC) WHERE state IN ('queued','running','waiting_user','reconciling','manual_review');
CREATE TABLE execution_links (
 run_id uuid NOT NULL,
 user_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('message','job','card','attempt','parent','object')),
 object_id text NOT NULL,
 version bigint NOT NULL DEFAULT 0 CHECK(version>=0),
 PRIMARY KEY(run_id,kind,object_id,version),
 FOREIGN KEY(run_id,user_id) REFERENCES execution_runs(id,user_id)
);
CREATE INDEX execution_link_lookup ON execution_links(user_id,kind,object_id);
CREATE UNIQUE INDEX execution_attempt_owner ON execution_links(object_id) WHERE kind='attempt';
CREATE TABLE execution_events (
 run_id uuid NOT NULL REFERENCES execution_runs(id),
 sequence bigint NOT NULL CHECK(sequence>0),
 fact_key text NOT NULL CHECK(length(fact_key) BETWEEN 1 AND 200),
 node_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('context','model','sources','validation','clarification','confirmation','queue','service','persistence','reconciliation','answer')),
 node_state text NOT NULL CHECK(node_state IN ('pending','running','waiting_user','waiting_service','succeeded','skipped','rejected','failed','unknown')),
 run_state text NOT NULL CHECK(run_state IN ('queued','running','waiting_user','reconciling','manual_review','completed','rejected','failed','cancelled')),
 detail_code text NOT NULL DEFAULT '' CHECK(detail_code IN ('','local','remote','fallback','manual_review','not_created','cancelled','expired','replaced','partial','created','submitted','approved')),
 service_code text NOT NULL DEFAULT '' CHECK(service_code IN ('','knowledge','access','time_off')),
 result_code text NOT NULL DEFAULT '' CHECK(result_code IN ('','answer','created','submitted','cancelled','rejected','unavailable','unknown','manual_review','not_created','partial','expired','replaced','fallback')),
 started_at timestamptz,
 finished_at timestamptz,
 observed_at timestamptz NOT NULL,
 PRIMARY KEY(run_id,sequence),
 UNIQUE(run_id,fact_key),
 CHECK(node_id=kind),
 CHECK(started_at IS NULL OR started_at<=observed_at),
 CHECK(finished_at IS NULL OR (started_at IS NOT NULL AND finished_at>=started_at AND finished_at<=observed_at))
);
