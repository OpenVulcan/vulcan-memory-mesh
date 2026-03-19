package aliyun_dashvector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type Store struct {
	endpoint   string
	apiKey     string
	namespace  string
	collection string
	client     *http.Client
}

func NewStore(endpoint, apiKey, namespace, collection string) *Store {
	return &Store{endpoint: strings.TrimRight(endpoint, "/"), apiKey: strings.TrimSpace(apiKey), namespace: namespace, collection: collection, client: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Store) Upsert(ctx context.Context, record domain.MemoryRecord) error {
	req := map[string]any{
		"namespace":  s.namespace,
		"collection": s.collection,
		"documents": []map[string]any{{
			"id":     record.ID,
			"vector": record.Vector,
			"fields": map[string]any{
				"text":       record.Text,
				"user_id":    record.Filter.UserID,
				"project_id": record.Filter.ProjectID,
				"space_id":   record.Filter.SpaceID,
			},
		}},
	}
	return s.post(ctx, s.endpoint+"/collections/"+s.collection+"/documents/upsert", req, nil)
}

func (s *Store) Search(ctx context.Context, vector []float32, topK int, filter domain.SearchFilter) ([]domain.MemoryHit, error) {
	req := map[string]any{
		"namespace":      s.namespace,
		"vector":         vector,
		"top_k":          topK,
		"filter":         map[string]any{"user_id": filter.UserID, "project_id": filter.ProjectID, "space_id": filter.SpaceID},
		"include_fields": []string{"text", "user_id", "project_id", "space_id"},
	}
	var resp struct {
		Output struct {
			Matches []struct {
				ID    string  `json:"id"`
				Score float64 `json:"score"`
				Fields struct {
					Text      string `json:"text"`
					UserID    string `json:"user_id"`
					ProjectID string `json:"project_id"`
					SpaceID   string `json:"space_id"`
				} `json:"fields"`
			} `json:"matches"`
		} `json:"output"`
	}
	if err := s.post(ctx, s.endpoint+"/collections/"+s.collection+"/documents/search", req, &resp); err != nil { return nil, err }
	hits := make([]domain.MemoryHit, 0, len(resp.Output.Matches))
	for _, item := range resp.Output.Matches {
		hits = append(hits, domain.MemoryHit{ID: item.ID, Text: item.Fields.Text, Score: item.Score, Filter: domain.SearchFilter{UserID: item.Fields.UserID, ProjectID: item.Fields.ProjectID, SpaceID: item.Fields.SpaceID}})
	}
	return hits, nil
}

func (s *Store) Shutdown(ctx context.Context) error {
	select { case <-ctx.Done(): return ctx.Err(); default: return nil }
}

func (s *Store) post(ctx context.Context, endpoint string, reqBody any, out any) error {
	body, err := json.Marshal(reqBody)
	if err != nil { return fmt.Errorf("marshal request: %w", err) }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil { return fmt.Errorf("build request: %w", err) }
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" { req.Header.Set("DashVector-Auth", s.apiKey) }
	resp, err := s.client.Do(req)
	if err != nil { return fmt.Errorf("do request: %w", err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil { return fmt.Errorf("read response: %w", err) }
	if resp.StatusCode >= 300 { return fmt.Errorf("dashvector http status %d: %s", resp.StatusCode, string(raw)) }
	if out == nil { return nil }
	if err := json.Unmarshal(raw, out); err != nil { return fmt.Errorf("decode response: %w", err) }
	return nil
}
