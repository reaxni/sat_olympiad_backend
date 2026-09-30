package server

import (
	"context"
	"satbackend/internal/exammedia"
)

func (s *Server) loadAssets(ctx context.Context, examID, sectionID string) (map[string]map[string]exammedia.StoredAsset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.question_id,a.id,a.mime_type,a.data FROM question_assets a JOIN questions q ON q.id=a.question_id WHERE q.exam_id=$1 AND ($2='' OR q.section_id=$2)`, examID, sectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]map[string]exammedia.StoredAsset{}
	for rows.Next() {
		var questionID string
		var a exammedia.StoredAsset
		if err := rows.Scan(&questionID, &a.ID, &a.MimeType, &a.Data); err != nil {
			return nil, err
		}
		if result[questionID] == nil {
			result[questionID] = map[string]exammedia.StoredAsset{}
		}
		result[questionID][a.ID] = a
	}
	return result, rows.Err()
}
