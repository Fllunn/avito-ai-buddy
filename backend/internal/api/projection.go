package api

import (
	"ai-buddy/backend/internal/actions"
	"ai-buddy/backend/platform/demo"
	"context"
	"encoding/json"
	"strings"
)

// authorizePayload checks persisted copies at the user-facing boundary. Worker
// authority continues to use the immutable, unfiltered storage records.
func (b *Business) authorizePayload(ctx context.Context, p demo.Profile, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	docs, err := b.Store.Documents(ctx, p)
	if err != nil {
		return err
	}
	resources, err := b.Store.Resources(ctx, p)
	if err != nil {
		return err
	}
	documentIDs, resourceIDs := map[string]bool{}, map[string]bool{}
	for _, d := range docs {
		documentIDs[d.ID] = true
	}
	for _, r := range resources {
		resourceIDs[r.ID] = true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return actions.ErrMissing
	}
	var visit func(any) error
	visit = func(value any) error {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				if err := visit(child); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, child := range value {
				switch key {
				case "resource_id":
					id, ok := child.(string)
					if !ok || id != "" && !resourceIDs[id] {
						return actions.ErrMissing
					}
				case "request_id":
					id, ok := child.(string)
					if !ok {
						return actions.ErrMissing
					}
					if strings.HasPrefix(id, "ACC-") {
						resource, err := b.requestResource(ctx, p.ID, id)
						if err != nil {
							return err
						}
						if !resourceIDs[resource] {
							return actions.ErrMissing
						}
					}
				case "sources", "resources":
					items, ok := child.([]any)
					if !ok {
						return actions.ErrMissing
					}
					for _, item := range items {
						entry, ok := item.(map[string]any)
						id, valid := entry["id"].(string)
						if !ok || !valid || key == "sources" && !documentIDs[id] || key == "resources" && !resourceIDs[id] {
							return actions.ErrMissing
						}
					}
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}

func (b *Business) requestResource(ctx context.Context, user, id string) (string, error) {
	objects, err := b.Store.Objects(ctx, user)
	if err != nil {
		return "", err
	}
	for _, object := range objects {
		if object.Service == "access" && object.ID == id {
			return object.ResourceID, nil
		}
	}
	// A cancellation draft may precede the first local refresh of its object.
	object, err := b.Tools.Remote.Get(ctx, user, "access", id)
	if err != nil {
		return "", err
	}
	if object.UserID != user || object.ID != id || object.Service != "access" {
		return "", actions.ErrMissing
	}
	return object.ResourceID, nil
}
