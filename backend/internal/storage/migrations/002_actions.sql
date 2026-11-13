CREATE TABLE conversations (
 user_id text PRIMARY KEY REFERENCES users(id),
 next_sequence bigint NOT NULL DEFAULT 1
);
CREATE TABLE messages (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id text NOT NULL REFERENCES conversations(user_id),
 sequence bigint NOT NULL,
 client_message_id uuid,
 role text NOT NULL CHECK(role IN ('user','assistant','system')),
 kind text NOT NULL,
 text text NOT NULL,
 intent text NOT NULL DEFAULT '',
 slots jsonb NOT NULL DEFAULT '{}',
 form_required boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 UNIQUE(user_id,sequence),
 UNIQUE(user_id,client_message_id)
);
CREATE TABLE cards (
 id uuid PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 type text NOT NULL CHECK(type IN ('access_create','access_cancel','time_off_create','time_off_cancel')),
 version integer NOT NULL CHECK(version>0),
 state text NOT NULL CHECK(state IN ('draft','discarded','expired','replaced','confirmed')),
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX cards_one_draft ON cards(user_id,type) WHERE state='draft';
CREATE TABLE card_versions (
 card_id uuid NOT NULL REFERENCES cards(id),
 version integer NOT NULL CHECK(version>0),
 parameters jsonb NOT NULL,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(card_id,version)
);
CREATE TABLE attempts (
 operational_created_at timestamptz NOT NULL DEFAULT now(),
 id uuid PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 card_id uuid NOT NULL,
 card_version integer NOT NULL,
 number integer NOT NULL CHECK(number>0),
 operation_key uuid NOT NULL UNIQUE,
 parameters jsonb NOT NULL,
 state text NOT NULL CHECK(state IN ('queued','sending','unavailable','unknown','succeeded','rejected','not_created','manual_review')),
 conflict_key text NOT NULL,
 start_date date,
 end_date date,
 result_source text CHECK (result_source IN ('local_validation','service')),
 may_have_sent boolean NOT NULL DEFAULT false,
 reconciliation_count integer NOT NULL DEFAULT 0 CHECK(reconciliation_count>=0),
 next_reconciliation_at timestamptz,
 result jsonb,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 FOREIGN KEY(card_id,card_version) REFERENCES card_versions(card_id,version),
 UNIQUE(card_id,card_version,number)
);
CREATE UNIQUE INDEX attempts_active_version ON attempts(card_id,card_version) WHERE state IN ('queued','sending','unavailable','unknown','manual_review');
CREATE UNIQUE INDEX attempts_resource_reservation ON attempts(user_id,conflict_key) WHERE conflict_key<>'' AND state IN ('queued','sending','unavailable','unknown','manual_review');
CREATE TABLE jobs (
 id uuid PRIMARY KEY,
 sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
 user_id text NOT NULL REFERENCES users(id),
 attempt_id uuid REFERENCES attempts(id),
 message_id bigint REFERENCES messages(id),
 kind text NOT NULL CHECK(kind IN ('message','send','reconcile','refresh')),
 state text NOT NULL CHECK(state IN ('queued','leased','done')),
 available_at timestamptz NOT NULL,
 lease_until timestamptz,
 lease_token uuid,
 created_at timestamptz NOT NULL,
 CHECK ((attempt_id IS NOT NULL)::integer + (message_id IS NOT NULL)::integer <= 1)
);
CREATE UNIQUE INDEX jobs_active_attempt ON jobs(attempt_id) WHERE state IN ('queued','leased');
CREATE UNIQUE INDEX jobs_one_message ON jobs(message_id) WHERE message_id IS NOT NULL;
CREATE INDEX jobs_schedule ON jobs(available_at) WHERE state='queued';
CREATE TABLE service_objects (
 service text NOT NULL,
 object_id text NOT NULL,
 user_id text NOT NULL REFERENCES users(id),
 service_version bigint NOT NULL CHECK(service_version>0),
 payload jsonb NOT NULL,
 checked_at timestamptz NOT NULL,
 PRIMARY KEY(service,object_id)
);
CREATE TABLE events (
 event_id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES users(id),
 attempt_id uuid REFERENCES attempts(id),
 code text NOT NULL,
 scenario text NOT NULL,
 outcome text NOT NULL,
 duration_ms bigint NOT NULL CHECK(duration_ms>=0),
 created_at timestamptz NOT NULL
);
CREATE TABLE notifications (
 service text NOT NULL,
 object_id text NOT NULL,
 service_version bigint NOT NULL,
 kind text NOT NULL,
 message_id bigint NOT NULL REFERENCES messages(id),
 PRIMARY KEY(service,object_id,service_version,kind)
);
