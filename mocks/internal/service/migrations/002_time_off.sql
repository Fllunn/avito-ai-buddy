CREATE SEQUENCE absence_number START 2001;
CREATE TABLE time_off_requests (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES actors(id),
 start_date date NOT NULL,
 end_date date NOT NULL,
 workdays integer NOT NULL CHECK(workdays BETWEEN 1 AND 5),
 approver_id text NOT NULL,
 status text NOT NULL CHECK(status IN ('submitted','in_review','approved','rejected','cancelled')),
 reason text,
 version bigint NOT NULL CHECK(version>0),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CHECK(end_date>=start_date)
);
CREATE INDEX absence_active ON time_off_requests(user_id,start_date,end_date) WHERE status IN ('submitted','in_review','approved');
