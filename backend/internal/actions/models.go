package actions

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var ErrConflict = errors.New("VERSION_CONFLICT")
var ErrMissing = errors.New("NOT_FOUND")
var ErrExpired = errors.New("CARD_EXPIRED")
var ErrInvalid = errors.New("VALIDATION_ERROR")
var ErrMessageConflict = errors.New("MESSAGE_ID_CONFLICT")
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func ValidID(value string) bool { return uuidPattern.MatchString(value) }
func ID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = (value[6] & 15) | 64
	value[8] = (value[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

type CardSummary struct {
	ResourceName string `json:"resource_name,omitempty"`
	ApproverID   string `json:"approver_id,omitempty"`
	Workdays     int    `json:"workdays,omitempty"`
	StartLabel   string `json:"start_label,omitempty"`
	EndLabel     string `json:"end_label,omitempty"`
	Demo         bool   `json:"demo"`
}
type Card struct {
	ExecutionIDs []string        `json:"execution_ids,omitempty"`
	Summary      *CardSummary    `json:"summary,omitempty"`
	Attempt      *Attempt        `json:"attempt,omitempty"`
	ID           string          `json:"id"`
	UserID       string          `json:"user_id"`
	Type         string          `json:"type"`
	Version      int             `json:"version"`
	State        string          `json:"state"`
	ExpiresAt    time.Time       `json:"expires_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Parameters   json.RawMessage `json:"parameters"`
}
type Message struct {
	ExecutionIDs []string          `json:"execution_ids,omitempty"`
	Metadata     json.RawMessage   `json:"metadata,omitempty"`
	Slots        map[string]string `json:"slots"`
	ID           int64             `json:"id"`
	Sequence     int64             `json:"sequence"`
	UserID       string            `json:"-"`
	ClientID     *string           `json:"client_message_id"`
	Role         string            `json:"role"`
	Kind         string            `json:"kind"`
	Text         string            `json:"text"`
	Intent       string            `json:"intent"`
	FormRequired bool              `json:"form_required"`
	CreatedAt    time.Time         `json:"created_at"`
}

type Attempt struct {
	OperationalCreatedAt time.Time       `json:"-"`
	ID                   string          `json:"id"`
	UserID               string          `json:"-"`
	CardID               string          `json:"card_id"`
	CardVersion          int             `json:"card_version"`
	Number               int             `json:"number"`
	OperationKey         string          `json:"-"`
	Parameters           json.RawMessage `json:"parameters"`
	State                string          `json:"state"`
	ConflictKey          string          `json:"-"`
	StartDate            *string         `json:"-"`
	EndDate              *string         `json:"-"`
	MayHaveSent          bool            `json:"-"`
	ReconciliationCount  int             `json:"-"`
	NextCheckAt          *time.Time      `json:"next_check_at,omitempty"`
	Result               json.RawMessage `json:"result,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"as_of"`
	JobID                string          `json:"job_id"`
}
type Reservation struct {
	Key                string
	StartDate, EndDate *string
}

var ErrActive = errors.New("CONFLICTING_ATTEMPT")
