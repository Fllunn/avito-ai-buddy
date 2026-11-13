package storage

import (
	"ai-buddy/backend/platform/demo"
	"context"
)

// Documents filters at the source, before text can enter a language-model context.
func (s *Store) Documents(ctx context.Context, p demo.Profile) ([]demo.Document, error) {
	result := []demo.Document{}
	if !p.Enabled {
		return result, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,title,team_id,roles,owner_id,url,stale,fragments,demo FROM documents WHERE $1=ANY(roles) AND (team_id IS NULL OR team_id=$2) ORDER BY id`, p.Role, p.TeamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d demo.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.TeamID, &d.Roles, &d.OwnerID, &d.URL, &d.Stale, &d.Fragments, &d.Demo); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func (s *Store) Resources(ctx context.Context, p demo.Profile) ([]demo.Resource, error) {
	result := []demo.Resource{}
	if !p.Enabled {
		return result, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,name,kind,levels,default_level,approver_id,recommended_teams,synonyms FROM resources WHERE kind<>'hidden' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r demo.Resource
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Levels, &r.DefaultLevel, &r.ApproverID, &r.RecommendedTeams, &r.Synonyms); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
