CREATE TABLE IF NOT EXISTS mock_schema_migrations(name text PRIMARY KEY);
CREATE TABLE actors(id text PRIMARY KEY,payload jsonb NOT NULL,enabled boolean NOT NULL DEFAULT true);
CREATE TABLE resources(id text PRIMARY KEY,payload jsonb NOT NULL);
CREATE SEQUENCE access_number START 1042;
CREATE TABLE access_requests(
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES actors(id),
 resource_id text NOT NULL REFERENCES resources(id),
 level text NOT NULL,
 justification text NOT NULL,
 approver_id text NOT NULL,
 status text NOT NULL CHECK(status IN ('created','in_review','approved','rejected','cancelled')),
 reason text,
 version bigint NOT NULL CHECK(version>0),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX access_one_open ON access_requests(user_id,resource_id) WHERE status IN ('created','in_review');
CREATE TABLE grants(user_id text NOT NULL REFERENCES actors(id),resource_id text NOT NULL REFERENCES resources(id),level text NOT NULL,granted_at timestamptz NOT NULL,source_request_id text NOT NULL REFERENCES access_requests(id),PRIMARY KEY(user_id,resource_id));
CREATE TABLE operations(
 operation_key uuid PRIMARY KEY,
 actor_id text NOT NULL REFERENCES actors(id),
 body_hash bytea,
 state text NOT NULL CHECK(state IN ('completed','not_created')),
 result_kind text CHECK(result_kind IN ('success','rejected')),
 http_status integer,
 body bytea
);
CREATE TABLE faults(service text PRIMARY KEY,mode text NOT NULL DEFAULT 'normal',delay_ms integer NOT NULL DEFAULT 0 CHECK(delay_ms>=0));
CREATE TABLE presenter_sessions(token_hash text PRIMARY KEY,expires_at timestamptz NOT NULL);
INSERT INTO faults(service) VALUES('access'),('time_off');
