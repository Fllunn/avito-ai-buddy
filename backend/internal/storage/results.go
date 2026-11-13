package storage

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/safety"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var objectID = regexp.MustCompile(`^(ACC|ABS)-[0-9]+$`)

func cleanResult(ctx context.Context, user string, result actions.Result) (actions.Result, error) {
	if result.Source == "" {
		result.Source = "service"
	}
	if result.Source != "service" && result.Source != "local_validation" {
		return result, actions.ErrContract
	}
	if result.Kind != actions.Success && result.Kind != actions.Rejected && result.Kind != actions.Unavailable {
		return result, actions.ErrContract
	}
	allowed := map[string]bool{"VALIDATION_FAILED": true, "VALIDATION_UNAVAILABLE": true, "VALIDATION_ERROR": true, "NOT_ALLOWED": true, "NOT_FOUND": true, "DUPLICATE_OPEN_REQUEST": true, "ALREADY_GRANTED": true, "LEVEL_NOT_ALLOWED": true, "INVALID_STATE": true, "VERSION_CONFLICT": true, "IDEMPOTENCY_CONFLICT": true, "OPERATION_CLOSED": true, "OVERLAPPING_REQUEST": true, "CANCELLATION_TOO_LATE": true, "SERVICE_UNAVAILABLE": true}
	if result.Kind != actions.Success {
		if !allowed[result.Code] {
			result.Code = "SERVICE_ERROR"
		}
		result.Object = nil
		return result, nil
	}
	if result.Source != "service" {
		return result, actions.ErrContract
	}
	if result.Object == nil {
		return result, actions.ErrContract
	}
	object := *result.Object
	if object.UserID != user || !objectID.MatchString(object.ID) || object.Version < 1 || !identifier.MatchString(object.ApproverID) || object.CreatedAt.IsZero() || object.UpdatedAt.IsZero() {
		return result, actions.ErrContract
	}
	if object.Service == "access" {
		if !strings.HasPrefix(object.ID, "ACC-") || !identifier.MatchString(object.ResourceID) || (object.Level != "read" && object.Level != "edit" && object.Level != "write") || utf8.RuneCountInString(object.Justification) < 10 || utf8.RuneCountInString(object.Justification) > 500 {
			return result, actions.ErrContract
		}
		switch object.Status {
		case "created", "in_review", "approved", "rejected", "cancelled":
		default:
			return result, actions.ErrContract
		}
	} else if object.Service == "time_off" {
		if !strings.HasPrefix(object.ID, "ABS-") || object.Workdays < 1 || object.Workdays > 5 {
			return result, actions.ErrContract
		}
		if _, err := time.Parse(time.DateOnly, object.StartDate); err != nil {
			return result, actions.ErrContract
		}
		if _, err := time.Parse(time.DateOnly, object.EndDate); err != nil {
			return result, actions.ErrContract
		}
		switch object.Status {
		case "submitted", "in_review", "approved", "rejected", "cancelled":
		default:
			return result, actions.ErrContract
		}
		object.Justification = ""
	} else {
		return result, actions.ErrContract
	}
	object.CreatedAt = object.CreatedAt.UTC()
	object.UpdatedAt = object.UpdatedAt.UTC()
	object.Justification = safety.Prepare(ctx, safety.Local{}, object.Justification).String()
	if object.Reason != nil {
		reason := safety.Prepare(ctx, safety.Local{}, *object.Reason).String()
		object.Reason = &reason
	}
	result.Object = &object
	return result, nil
}
func (s *Store) Finish(ctx context.Context, job actions.Work, result actions.Result, now time.Time) error {
	result, err := cleanResult(ctx, job.Attempt.UserID, result)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := lockWork(ctx, tx, job); err != nil {
		return err
	}
	state := "succeeded"
	if result.Kind == actions.Rejected {
		state = "rejected"
	}
	if result.Kind == actions.Unavailable {
		state = "unavailable"
	}
	if result.Object != nil {
		if err := saveObject(ctx, tx, job, *result.Object, now); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE attempts SET state=$2,result_source=$3,result=$4,next_reconciliation_at=NULL,may_have_sent=CASE WHEN $2='unavailable' THEN false ELSE may_have_sent END,updated_at=$5 WHERE id=$1`, job.Attempt.ID, state, result.Source, payload, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state='done',lease_token=NULL,lease_until=NULL WHERE id=$1`, job.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO events(event_id,user_id,attempt_id,code,scenario,outcome,duration_ms,created_at) VALUES($1,$2,$3,'action_result',$4,$5,$6,$7) ON CONFLICT DO NOTHING`, job.Attempt.ID+":"+state, job.Attempt.UserID, job.Attempt.ID, job.CardType, state, max(int64(0), s.OperationalClock.Now().Sub(job.Attempt.OperationalCreatedAt).Milliseconds()), now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func saveObject(ctx context.Context, tx pgx.Tx, job actions.Work, object actions.Object, now time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, object.Service+":"+object.ID); err != nil {
		return err
	}
	var existingVersion int64
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT service_version,payload FROM service_objects WHERE service=$1 AND object_id=$2 FOR UPDATE`, object.Service, object.ID).Scan(&existingVersion, &payload)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil {
		if existingVersion > object.Version {
			return nil
		}
		if existingVersion == object.Version {
			var existing actions.Object
			if json.Unmarshal(payload, &existing) != nil || !reflect.DeepEqual(existing, object) {
				return actions.ErrContract
			}
			_, err = tx.Exec(ctx, `UPDATE service_objects SET checked_at=$3 WHERE service=$1 AND object_id=$2`, object.Service, object.ID, now)
			return err
		}
	}
	payload, err = json.Marshal(object)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO service_objects(service,object_id,user_id,service_version,payload,checked_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(service,object_id) DO UPDATE SET service_version=EXCLUDED.service_version,payload=EXCLUDED.payload,checked_at=EXCLUDED.checked_at`, object.Service, object.ID, object.UserID, object.Version, payload, now); err != nil {
		return err
	}
	if object.Service == "access" {
		status := map[string]string{"created": "waiting", "in_review": "waiting", "approved": "done", "rejected": "attention", "cancelled": "not_started"}[object.Status]
		if _, err = tx.Exec(ctx, `UPDATE plan_steps SET status=$2,source=$3,updated_at=$4,version=version+1 WHERE intern_id=$1 AND code='P6' AND EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND $5='space-'||u.team_id)`, object.UserID, status, "сервис заявок, заявка "+object.ID, object.UpdatedAt, object.ResourceID); err != nil {
			return err
		}
	}
	messageID, err := appendSystemMessage(ctx, tx, object.UserID, object.ID+": "+statusLabel(object.Status)+". Демонстрационный сервис.", now)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notifications(service,object_id,service_version,kind,message_id) VALUES($1,$2,$3,'status',$4) ON CONFLICT DO NOTHING`, object.Service, object.ID, object.Version, messageID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(event_id,user_id,attempt_id,code,scenario,outcome,duration_ms,created_at) VALUES($1,$2,$3,'service_status',$4,$5,0,$6) ON CONFLICT DO NOTHING`, object.Service+":"+object.ID+":"+strconv.FormatInt(object.Version, 10), object.UserID, optionalID(job.Attempt.ID), object.Service, object.Status, now)
	return err
}
func statusLabel(status string) string {
	return map[string]string{"created": "Заявка создана", "submitted": "Запрос отправлен", "in_review": "На рассмотрении", "approved": "Согласовано", "rejected": "Отклонено", "cancelled": "Отменено"}[status]
}
func appendSystemMessage(ctx context.Context, tx pgx.Tx, user, text string, now time.Time) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO conversations(user_id) VALUES($1) ON CONFLICT DO NOTHING`, user); err != nil {
		return 0, err
	}
	var sequence int64
	if err := tx.QueryRow(ctx, `UPDATE conversations SET next_sequence=next_sequence+1 WHERE user_id=$1 RETURNING next_sequence-1`, user).Scan(&sequence); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO messages(user_id,sequence,role,kind,text,created_at) VALUES($1,$2,'assistant','result',$3,$4) RETURNING id`, user, sequence, text, now).Scan(&id)
	return id, err
}

func optionalID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func (s *Store) ApplyObject(ctx context.Context, user string, object actions.Object, now time.Time) error {
	result, err := cleanResult(ctx, user, actions.Result{Kind: actions.Success, Source: "service", Object: &object})
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err := saveObject(ctx, tx, actions.Work{Attempt: actions.Attempt{UserID: user}}, *result.Object, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
