package api

import (
	"ai-buddy/backend/internal/actions"
	"context"
)

// accessPreflight checks existing state before asking for level or justification.
func (b *Business) accessPreflight(ctx context.Context, user, resource string) string {
	grants, e := b.Tools.Remote.Grants(ctx, user)
	if e != nil {
		return "Не удалось проверить выданные доступы. Карточка не создана; попробуй позже."
	}
	for _, g := range grants {
		if g.ResourceID == resource {
			return "Доступ уже выдан. Если ресурс не открывается, уточни у своего наставника; новая заявка не нужна."
		}
	}
	requests, e := b.Tools.Remote.List(ctx, user, "access")
	if e != nil {
		return "Не удалось проверить заявки. Карточка не создана; попробуй позже."
	}
	for _, r := range requests {
		if r.ResourceID == resource && (r.Status == "created" || r.Status == "in_review") {
			return "Уже есть открытая заявка " + r.ID + ". Проверь её статус или подготовь отмену; новая карточка не создана."
		}
	}
	if e = b.Store.Conflict(ctx, user, actions.Reservation{Key: "resource:" + resource}); e != nil {
		return "Предыдущий исход по ресурсу ещё уточняется. Сначала проверь попытку; новая карточка не создана."
	}
	return ""
}
