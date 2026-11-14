package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/internal/tools"
	"ai-buddy/backend/platform/demo"
	"ai-buddy/backend/platform/safety"
	"context"
)

const reapplyQuery = "Подтверждаю повторную подачу"

type reapplyError struct{ ID, Reason string }

func (reapplyError) Error() string { return "REAPPLY_CONFIRMATION_REQUIRED" }
func (b *Business) priorRejection(ctx context.Context, p demo.Profile, kind string, params tools.Parameters) (*actions.Object, error) {
	service := "access"
	if kind == "time_off_create" {
		service = "time_off"
	}
	items, e := b.Tools.Remote.List(ctx, p.ID, service)
	if e != nil {
		return nil, e
	}
	var latest *actions.Object
	for i := range items {
		item := items[i]
		matches := service == "access" && item.ResourceID == params.ResourceID || service == "time_off" && item.StartDate <= params.EndDate && item.EndDate >= params.StartDate
		if !matches {
			continue
		}
		if latest == nil || item.UpdatedAt.After(latest.UpdatedAt) {
			copy := item
			latest = &copy
		}
	}
	if latest == nil || latest.Status != "rejected" {
		return nil, nil
	}
	if latest.Reason != nil {
		reason := safety.Prepare(ctx, safety.Local{}, *latest.Reason).String()
		latest.Reason = &reason
	}
	return latest, nil
}
func rejectionReason(object *actions.Object) string {
	if object.Reason == nil {
		return "Причина не указана."
	}
	return *object.Reason
}
