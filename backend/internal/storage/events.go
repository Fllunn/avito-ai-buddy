package storage

import (
	"ai-buddy/backend/internal/actions"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

func recordEvent(ctx context.Context, tx pgx.Tx, user, id, code, scenario, outcome string, duration int64, now time.Time, attempts ...string) error {
	var attempt any
	if len(attempts) > 0 {
		attempt = attempts[0]
	}
	_, e := tx.Exec(ctx, `INSERT INTO events(event_id,user_id,code,scenario,outcome,duration_ms,created_at,role,team,period,session_id,attempt_id) SELECT $1,id,$3,$4,$5,$6,$7::timestamptz,role,team_id,GREATEST(1,1+floor(($7::timestamptz::date-start_date)/7.0)::int),'conversation:'||id,$8::uuid FROM users WHERE id=$2 ON CONFLICT DO NOTHING`, id, user, code, scenario, outcome, max(int64(0), duration), now, attempt)
	return e
}
func (s *Store) ExpireDrafts(ctx context.Context, now time.Time) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	rows, e := tx.Query(ctx, `UPDATE cards SET state='expired',updated_at=$1 WHERE state='draft' AND expires_at<=$1 RETURNING id::text,user_id,type,version`, now)
	if e != nil {
		return e
	}
	type expired struct {
		id, user, kind string
		version        int
	}
	items := []expired{}
	for rows.Next() {
		var item expired
		if e = rows.Scan(&item.id, &item.user, &item.kind, &item.version); e != nil {
			rows.Close()
			return e
		}
		items = append(items, item)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, item := range items {
		if e = s.cardExecutionFact(ctx, tx, actions.Card{ID: item.id, UserID: item.user, Type: item.kind, Version: item.version}, "expired"); e != nil {
			return e
		}
		if e = recordEvent(ctx, tx, item.user, "card:"+item.id+":"+strconv.Itoa(item.version)+":expired", "card_expired", item.kind, "expired", 0, now); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func recordReply(ctx context.Context, tx pgx.Tx, user string, messageID int64, result, state json.RawMessage, now time.Time) error {
	var dialogue struct {
		LastIntent string `json:"last_intent"`
	}
	_ = json.Unmarshal(state, &dialogue)
	scenario := dialogue.LastIntent
	if scenario == "" {
		scenario = "unclear"
	}
	key := "reply:" + strconv.FormatInt(messageID, 10)
	if e := recordEvent(ctx, tx, user, key+":intent", "intent_classified", scenario, "ok", 0, now); e != nil {
		return e
	}
	var reply struct {
		Items []struct {
			Answer *struct {
				Sources []json.RawMessage `json:"sources"`
				Code    string            `json:"code"`
			} `json:"answer"`
			Contact json.RawMessage   `json:"contact"`
			Fields  map[string]string `json:"fields"`
			Access  json.RawMessage   `json:"access_form"`
			Absence json.RawMessage   `json:"time_off_form"`
		} `json:"items"`
		Code string `json:"code"`
	}
	if json.Unmarshal(result, &reply) != nil {
		return nil
	}
	if reply.Code != "" {
		if e := recordEvent(ctx, tx, user, key+":llm", "llm_error", scenario, "fallback", 0, now); e != nil {
			return e
		}
	}
	for i, item := range reply.Items {
		id := key + ":" + strconv.Itoa(i)
		outcome := "template"
		if item.Answer != nil {
			outcome = "no_source"
			if len(item.Answer.Sources) > 0 {
				outcome = "has_source"
			}
		}
		if len(item.Contact) > 0 {
			outcome = "contact"
		}
		code := "answer_shown"
		if len(item.Fields) > 0 || len(item.Access) > 0 || len(item.Absence) > 0 {
			code = "clarification_asked"
			outcome = "parameters"
		}
		if e := recordEvent(ctx, tx, user, id, code, scenario, outcome, 0, now); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) RecordCall(ctx context.Context, user, scenario, outcome string, duration int64) {
	if scenario != "fake:recognize" && scenario != "fake:fragments" && scenario != "openrouter:recognize" && scenario != "openrouter:fragments" {
		return
	}
	if outcome != "ok" && outcome != "error" && outcome != "timeout" {
		return
	}
	background, cancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
	defer cancel()
	tx, e := s.Pool.Begin(background)
	if e != nil {
		return
	}
	defer tx.Rollback(context.WithoutCancel(background))
	if e = recordEvent(background, tx, user, actions.ID(), "llm_call", scenario, outcome, duration, s.OperationalClock.Now()); e == nil {
		_ = tx.Commit(background)
	}
}

func (s *Store) RecordLocal(ctx context.Context, user, scenario string, duration int64) {
	if scenario != "plan" && scenario != "history" && scenario != "card" {
		return
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e = recordEvent(ctx, tx, user, actions.ID(), "local_request", scenario, "ok", duration, s.OperationalClock.Now()); e == nil {
		_ = tx.Commit(ctx)
	}
}
