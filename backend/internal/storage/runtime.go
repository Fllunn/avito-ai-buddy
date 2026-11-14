package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/authz"
	"ai-buddy/backend/internal/onboarding"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

type Job struct {
	ExecutionIDs []string        `json:"execution_ids,omitempty"`
	ID           string          `json:"id"`
	State        string          `json:"state"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
}

func (s *Store) Job(ctx context.Context, user, id string) (Job, error) {
	if !actions.ValidID(id) {
		return Job{}, actions.ErrMissing
	}
	var j Job
	e := s.Pool.QueryRow(ctx, `SELECT id::text,CASE state WHEN 'done' THEN CASE WHEN error_code IS NULL THEN 'completed' ELSE 'failed' END WHEN 'leased' THEN 'running' ELSE 'queued' END,result,COALESCE(error_code,'') FROM jobs WHERE id=$1 AND user_id=$2`, id, user).Scan(&j.ID, &j.State, &j.Result, &j.Error)
	if e == pgx.ErrNoRows {
		return j, actions.ErrMissing
	}
	if e == nil {
		j.ExecutionIDs, e = s.ExecutionIDs(ctx, user, "job", id)
	}
	return j, e
}
func (s *Store) MessageJob(ctx context.Context, user string, message int64) (string, error) {
	var id string
	e := s.Pool.QueryRow(ctx, `SELECT id::text FROM jobs WHERE user_id=$1 AND message_id=$2`, user, message).Scan(&id)
	return id, e
}
func (s *Store) Cards(ctx context.Context, user string, now time.Time) ([]actions.Card, error) {
	rows, e := s.Pool.Query(ctx, `SELECT `+cardColumns+` FROM cards c JOIN card_versions v ON v.card_id=c.id AND v.version=c.version WHERE c.user_id=$1 AND (c.state='draft' OR EXISTS(SELECT 1 FROM attempts a WHERE a.card_id=c.id AND a.state IN ('queued','sending','unavailable','unknown','manual_review'))) ORDER BY c.updated_at`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []actions.Card{}
	for rows.Next() {
		c, e := scanCard(rows)
		if e != nil {
			return nil, e
		}
		if c.State == "draft" && !now.Before(c.ExpiresAt) {
			continue
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range items {
		a, err := scanAttempt(s.Pool.QueryRow(ctx, `SELECT `+attemptColumns+` FROM attempts a WHERE a.user_id=$1 AND a.card_id=$2 ORDER BY a.number DESC LIMIT 1`, user, items[i].ID))
		if err == nil {
			items[i].Attempt = &a
		} else if err != actions.ErrMissing {
			return nil, err
		}
	}
	return items, nil
}
func (s *Store) Objects(ctx context.Context, user string) ([]actions.Object, error) {
	rows, e := s.Pool.Query(ctx, `SELECT payload FROM service_objects WHERE user_id=$1 ORDER BY service,object_id`, user)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []actions.Object{}
	for rows.Next() {
		var raw []byte
		var object actions.Object
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &object); e != nil {
			return nil, e
		}
		items = append(items, object)
	}
	return items, rows.Err()
}
func (s *Store) Conflict(ctx context.Context, user string, reservation actions.Reservation) error {
	var exists bool
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attempts WHERE user_id=$1 AND state IN ('queued','sending','unavailable','unknown','manual_review') AND (($2<>'' AND conflict_key=$2) OR ($3::date IS NOT NULL AND start_date<=$4::date AND end_date>=$3::date)))`, user, reservation.Key, reservation.StartDate, reservation.EndDate).Scan(&exists)
	if e != nil {
		return e
	}
	if exists {
		return actions.ErrActive
	}
	return nil
}
func (s *Store) Welcome(ctx context.Context, p demo.Profile, now time.Time) error {
	steps, e := s.Steps(ctx, p.ID)
	if e != nil {
		return e
	}
	plan := onboarding.Project(p, steps, now, now.Location())
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, e = tx.Exec(ctx, `INSERT INTO conversations(user_id) VALUES($1) ON CONFLICT DO NOTHING`, p.ID); e != nil {
		return e
	}
	var welcomed bool
	if e = tx.QueryRow(ctx, `SELECT welcomed FROM conversations WHERE user_id=$1 FOR UPDATE`, p.ID).Scan(&welcomed); e != nil {
		return e
	}
	if !welcomed {
		text := "Привет, " + p.Name + "! Я AI-buddy — помогу освоиться, найти инструкцию и оформить собственный доступ или отгул. Данные и правила демонстрационные; помощник не заменяет наставника."
		if p.Role == "intern" {
			team := "команда не назначена"
			if p.TeamID != nil {
				team = map[string]string{"payments": "Платежи", "search": "Поиск"}[*p.TeamID]
			}
			text += " Сейчас у тебя " + strconv.Itoa(plan.Week) + "-я неделя, команда «" + team + "»."
			for _, contact := range demo.Contacts() {
				if p.BuddyID != nil && contact.ID == *p.BuddyID {
					text += " Наставник — " + contact.Name + "."
				}
				if p.ManagerID != nil && contact.ID == *p.ManagerID {
					text += " Руководитель — " + contact.Name + "."
				}
			}
			if plan.ReadOnly {
				text += " До начала стажировки план доступен только для чтения."
			} else if len(plan.NextSteps) > 0 {
				text += " Ближайший шаг: «" + plan.NextSteps[0].Title + "»."
			}
		} else {
			text += " Тебе доступны только собственные назначения. Планы и переписку других сотрудников я не раскрываю."
		}
		if _, e = appendSystemMessage(ctx, tx, p.ID, text, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE conversations SET welcomed=true WHERE user_id=$1`, p.ID); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

type MessageWork struct {
	JobID, LeaseToken string
	ExecutionID       string
	Message           actions.Message
}

func (s *Store) ClaimMessage(ctx context.Context, now time.Time, lease time.Duration) (MessageWork, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return MessageWork{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var work MessageWork
	var id int64
	e = tx.QueryRow(ctx, `SELECT j.id::text,j.message_id FROM jobs j WHERE j.message_id IS NOT NULL AND ((j.state='queued' AND j.available_at<=$1) OR (j.state='leased' AND j.lease_until<=$1)) AND NOT EXISTS(SELECT 1 FROM jobs prior WHERE prior.user_id=j.user_id AND prior.message_id IS NOT NULL AND prior.sequence<j.sequence AND prior.state<>'done') ORDER BY j.sequence FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&work.JobID, &id)
	if e == pgx.ErrNoRows {
		return work, actions.ErrNoWork
	}
	if e != nil {
		return work, e
	}
	work.Message, e = scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE id=$1`, id))
	if e != nil {
		return work, e
	}
	work.ExecutionID, e = routeFor(ctx, tx, work.Message.UserID, "job", work.JobID)
	if e != nil {
		return work, e
	}
	work.LeaseToken = actions.ID()
	if _, e = tx.Exec(ctx, `UPDATE jobs SET state='leased',lease_token=$2,lease_until=$3 WHERE id=$1`, work.JobID, work.LeaseToken, now.Add(lease)); e != nil {
		return work, e
	}
	return work, tx.Commit(ctx)
}
func (s *Store) CompleteMessage(ctx context.Context, work MessageWork, text safety.Text, result any, code string, now time.Time, state json.RawMessage) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if e := lockWork(ctx, tx, actions.Work{JobID: work.JobID, LeaseToken: work.LeaseToken}); e != nil {
		return e
	}
	messageID, e := appendSystemMessage(ctx, tx, work.Message.UserID, text.String(), now)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE messages SET metadata=$2 WHERE id=$1`, messageID, raw); e != nil {
		return e
	}
	var failure any
	if code != "" {
		failure = code
	}
	if e = recordReply(ctx, tx, work.Message.UserID, messageID, raw, state, now); e != nil {
		return e
	}
	if len(state) > 0 {
		if _, e = tx.Exec(ctx, `INSERT INTO dialogue_state(user_id,payload) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET payload=EXCLUDED.payload`, work.Message.UserID, state); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE jobs SET state='done',lease_token=NULL,lease_until=NULL,result=$2,error_code=$3,payload=jsonb_build_object('intent',($4::jsonb)->>'last_intent') WHERE id=$1`, work.JobID, raw, failure, state); e != nil {
		return e
	}
	if e = s.completeMessageExecution(ctx, tx, work, messageID, code, state); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type StepConfirmation struct {
	ID        string    `json:"confirmation_id"`
	Summary   string    `json:"summary"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Store) StepConfirmation(ctx context.Context, p demo.Profile, id string, version int64, target string, now time.Time, ttl time.Duration, location *time.Location) (StepConfirmation, error) {
	if target != "done" && target != "in_progress" {
		return StepConfirmation{}, actions.ErrInvalid
	}
	steps, e := s.Steps(ctx, p.ID)
	if e != nil {
		return StepConfirmation{}, e
	}
	var chosen *demo.Step
	for i := range steps {
		if steps[i].ID == id {
			chosen = &steps[i]
			break
		}
	}
	if chosen == nil {
		return StepConfirmation{}, actions.ErrMissing
	}
	if e = authz.ChangeStep(p, *chosen, steps, now, location); e != nil {
		return StepConfirmation{}, e
	}
	if chosen.Version != version {
		return StepConfirmation{}, actions.ErrConflict
	}
	c := StepConfirmation{actions.ID(), "Изменить шаг «" + chosen.Title + "» на «" + map[string]string{"done": "Выполнен", "in_progress": "В работе"}[target] + "»?", now.Add(ttl)}
	_, e = s.Pool.Exec(ctx, `INSERT INTO step_confirmations(id,user_id,step_id,expected_version,target_state,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, c.ID, p.ID, id, version, target, c.ExpiresAt)
	return c, e
}
func (s *Store) ConfirmStep(ctx context.Context, user, stepID, confirmationID string, now time.Time, location *time.Location) (demo.Step, error) {
	if !actions.ValidID(confirmationID) {
		return demo.Step{}, actions.ErrMissing
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return demo.Step{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	p, e := scanProfile(tx.QueryRow(ctx, `SELECT `+profileColumns+` FROM users WHERE id=$1 FOR UPDATE`, user))
	if e != nil {
		return demo.Step{}, actions.ErrMissing
	}
	var version int64
	var target string
	var expires time.Time
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT expected_version,target_state,expires_at,result FROM step_confirmations WHERE id=$1 AND user_id=$2 AND step_id=$3 FOR UPDATE`, confirmationID, user, stepID).Scan(&version, &target, &expires, &raw)
	if e == pgx.ErrNoRows {
		return demo.Step{}, actions.ErrMissing
	}
	if e != nil {
		return demo.Step{}, e
	}
	rows, e := tx.Query(ctx, `SELECT id,intern_id,code,title,period,owner_id,closure,status,dependencies,source,to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),version FROM plan_steps WHERE intern_id IN (SELECT intern_id FROM plan_steps WHERE id=$1) ORDER BY id FOR SHARE`, stepID)
	if e != nil {
		return demo.Step{}, e
	}
	steps := []demo.Step{}
	for rows.Next() {
		var step demo.Step
		if e = rows.Scan(&step.ID, &step.InternID, &step.Code, &step.Title, &step.Period, &step.OwnerID, &step.Closure, &step.Status, &step.Dependencies, &step.Source, &step.UpdatedAt, &step.Version); e != nil {
			rows.Close()
			return step, e
		}
		steps = append(steps, step)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return demo.Step{}, e
	}
	var chosen demo.Step
	for _, step := range steps {
		if step.ID == stepID {
			chosen = step
		}
	}
	if len(raw) > 0 {
		if !p.Enabled || chosen.OwnerID != p.ID {
			return chosen, authz.ErrNotFound
		}
		if chosen.Closure != "manual" {
			return chosen, authz.ErrNotAllowed
		}
		if p.Role == "intern" {
			if e = authz.ServiceAction(p, now, location); e != nil {
				return chosen, e
			}
		}
		var previous demo.Step
		e = json.Unmarshal(raw, &previous)
		return previous, e
	}
	if e = authz.ChangeStep(p, chosen, steps, now, location); e != nil {
		return chosen, e
	}
	if !now.Before(expires) {
		return chosen, actions.ErrExpired
	}
	if chosen.Version != version {
		return chosen, actions.ErrConflict
	}
	chosen.Status = target
	chosen.Version++
	chosen.Source = "отметил(а) " + p.Name
	chosen.UpdatedAt = now.UTC().Format(time.RFC3339)
	if _, e = tx.Exec(ctx, `UPDATE plan_steps SET status=$2,version=$3,source=$4,updated_at=$5 WHERE id=$1`, stepID, target, chosen.Version, chosen.Source, now); e != nil {
		return chosen, e
	}
	raw, _ = json.Marshal(chosen)
	if _, e = tx.Exec(ctx, `UPDATE step_confirmations SET result=$2 WHERE id=$1`, confirmationID, raw); e != nil {
		return chosen, e
	}
	if _, e = appendSystemMessage(ctx, tx, user, "Шаг «"+chosen.Title+"»: "+map[string]string{"done": "Выполнен", "in_progress": "В работе"}[target]+". "+chosen.Source, now); e != nil {
		return chosen, e
	}
	if e = recordEvent(ctx, tx, user, "step:"+confirmationID, "step_marked", "onboarding", target, 0, now); e != nil {
		return chosen, e
	}
	return chosen, tx.Commit(ctx)
}

func scanProfile(row pgx.Row) (demo.Profile, error) {
	var p demo.Profile
	e := row.Scan(&p.ID, &p.Name, &p.Role, &p.TeamID, &p.StartDate, &p.BuddyID, &p.ManagerID, &p.CuratorID, &p.PolicyVersion, &p.Enabled)
	return p, e
}

func (s *Store) RefreshJob(ctx context.Context, user string) (string, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, e = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user); e != nil {
		return "", e
	}
	var id string
	e = tx.QueryRow(ctx, `SELECT id::text FROM jobs WHERE user_id=$1 AND kind='refresh' AND state IN ('queued','leased')`, user).Scan(&id)
	if e == pgx.ErrNoRows {
		id = actions.ID()
		_, e = tx.Exec(ctx, `INSERT INTO jobs(id,user_id,kind,state,available_at,created_at) VALUES($1,$2,'refresh','queued',$3,$3)`, id, user, s.OperationalClock.Now())
	}
	if e != nil {
		return "", e
	}
	return id, tx.Commit(ctx)
}
func (s *Store) ClaimRefresh(ctx context.Context, now time.Time, lease time.Duration) (MessageWork, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return MessageWork{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var work MessageWork
	e = tx.QueryRow(ctx, `SELECT id::text,user_id FROM jobs WHERE kind='refresh' AND ((state='queued' AND available_at<=$1) OR (state='leased' AND lease_until<=$1)) ORDER BY sequence FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&work.JobID, &work.Message.UserID)
	if e == pgx.ErrNoRows {
		return work, actions.ErrNoWork
	}
	if e != nil {
		return work, e
	}
	work.LeaseToken = actions.ID()
	if _, e = tx.Exec(ctx, `UPDATE jobs SET state='leased',lease_token=$2,lease_until=$3 WHERE id=$1`, work.JobID, work.LeaseToken, now.Add(lease)); e != nil {
		return work, e
	}
	return work, tx.Commit(ctx)
}
func (s *Store) CompleteRefresh(ctx context.Context, work MessageWork, code string) error {
	var failure any
	if code != "" {
		failure = code
	}
	tag, e := s.Pool.Exec(ctx, `UPDATE jobs SET state='done',error_code=$3,lease_token=NULL,lease_until=NULL WHERE id=$1 AND state='leased' AND lease_token=$2`, work.JobID, work.LeaseToken, failure)
	if e == nil && tag.RowsAffected() != 1 {
		return actions.ErrLeaseLost
	}
	return e
}

func (s *Store) Dialogue(ctx context.Context, user string) (json.RawMessage, error) {
	var raw []byte
	e := s.Pool.QueryRow(ctx, `SELECT payload FROM dialogue_state WHERE user_id=$1`, user).Scan(&raw)
	if e == pgx.ErrNoRows {
		return json.RawMessage(`{}`), nil
	}
	return raw, e
}

// OfferNext emits one durable invitation when the preceding action has a known
// outcome. Unknown/manual-review attempts keep the next action blocked.
func (s *Store) OfferNext(ctx context.Context, now time.Time) (bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var user string
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT d.user_id,d.payload FROM dialogue_state d JOIN cards c ON c.id::text=d.payload->>'card_id' WHERE jsonb_array_length(COALESCE(d.payload->'pending','[]'))>0 AND COALESCE(d.payload->>'stage','')<>'await_next' AND (c.state IN ('discarded','expired','replaced') OR EXISTS(SELECT 1 FROM attempts a WHERE a.card_id=c.id AND a.state IN ('succeeded','rejected','not_created') AND a.number=(SELECT max(number) FROM attempts WHERE card_id=c.id))) ORDER BY d.user_id FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(&user, &raw)
	if e == pgx.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var state map[string]json.RawMessage
	if e = json.Unmarshal(raw, &state); e != nil {
		return false, e
	}
	var pending []struct {
		Name string `json:"name"`
	}
	if e = json.Unmarshal(state["pending"], &pending); e != nil || len(pending) == 0 {
		return false, e
	}
	name := map[string]string{"access_request": "доступу", "time_off_request": "отгулу", "find_doc": "поиску инструкции", "next_step": "следующему шагу"}[pending[0].Name]
	if name == "" {
		name = "следующему вопросу"
	}
	if len(state["transfer"]) > 0 {
		var transfer struct {
			FirstCardID string `json:"first_card_id"`
		}
		if e = json.Unmarshal(state["transfer"], &transfer); e != nil {
			return false, e
		}
		var status string
		e = tx.QueryRow(ctx, `SELECT state FROM attempts WHERE card_id=$1 ORDER BY number DESC LIMIT 1`, transfer.FirstCardID).Scan(&status)
		if e != nil && e != pgx.ErrNoRows {
			return false, e
		}
		if status != "succeeded" {
			var pendingRoutes []string
			_ = json.Unmarshal(state["pending_executions"], &pendingRoutes)
			if e = declineExecutions(ctx, tx, user, pendingRoutes, "transfer:stopped", s.OperationalClock.Now()); e != nil {
				return false, e
			}
			state["pending_executions"] = json.RawMessage(`[]`)
			state["pending"] = json.RawMessage(`[]`)
			delete(state, "transfer")
			if _, e = appendSystemMessage(ctx, tx, user, "Первое действие переноса не завершилось успехом. Второе не выполняется; проверь сохранённые запросы.", now); e != nil {
				return false, e
			}
		}
	}
	if string(state["pending"]) != "[]" {
		messageID, e := appendSystemMessage(ctx, tx, user, "Предыдущее действие завершено. Перейдём к "+name+"? Ответь «да» или «позже».", now)
		if e != nil {
			return false, e
		}
		metadata, _ := json.Marshal(map[string]any{"items": []any{map[string]string{"prompt": "next_intent"}}, "mode": "template"})
		if _, e = tx.Exec(ctx, `UPDATE messages SET metadata=$2 WHERE id=$1`, messageID, metadata); e != nil {
			return false, e
		}
	}
	state["stage"] = json.RawMessage(`"await_next"`)
	raw, _ = json.Marshal(state)
	if _, e = tx.Exec(ctx, `UPDATE dialogue_state SET payload=$2 WHERE user_id=$1`, user, raw); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
