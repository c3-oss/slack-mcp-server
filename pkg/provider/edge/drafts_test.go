package edge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type capturedDraftRequest struct {
	path, contentType, auth string
	body                    []byte
	form                    url.Values
}

func TestUnitDraftsPadTs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"1700000000.1", "1700000000.1000000"}, {"1700000000.1234567", "1700000000.1234567"}, {"1700000000", "1700000000"}} {
		require.Equal(t, tc.want, padDraftTs(tc.in))
	}
}

func TestUnitDraftsResponseAcceptsNumericDates(t *testing.T) {
	var response DraftsListResponse
	err := json.Unmarshal([]byte(`{"ok":true,"drafts":[{"id":"D1","last_updated_ts":"1700000000.1234567","date_created":1700000000,"date_scheduled":1700000100,"destinations":[{"channel_id":"C1"},{"channel_id":"C2","thread_ts":"1.2","broadcast":true}]}]}`), &response)
	require.NoError(t, err)
	require.Equal(t, "1700000000", response.Drafts[0].DateCreated.String())
	require.Len(t, response.Drafts[0].Destinations, 2)
	require.Empty(t, response.Drafts[0].Destinations[0].ThreadTS)
}

func newDraftClient(t *testing.T, response string, capture *capturedDraftRequest) *Client {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.path, capture.contentType, capture.auth = r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		capture.body, _ = io.ReadAll(r.Body)
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
			capture.form, _ = url.ParseQuery(string(capture.body))
		}
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return &Client{cl: server.Client(), webclientAPI: server.URL + "/api/", token: "token", tape: nopTape{}}
}

func TestUnitDraftsCreateJSONAndUUID(t *testing.T) {
	var first, second capturedDraftRequest
	client := newDraftClient(t, `{"ok":true,"draft":{"id":"D1"}}`, &first)
	got, err := client.DraftsCreate(context.Background(), DraftInput{ChannelID: "C1", Text: "hello", FileIDs: nil})
	require.NoError(t, err)
	require.Equal(t, "D1", got.ID)
	require.Equal(t, "/api/drafts.create", first.path)
	require.Equal(t, "application/json", first.contentType)
	require.Equal(t, "Bearer token", first.auth)
	var body map[string]any
	require.NoError(t, json.Unmarshal(first.body, &body))
	require.NotContains(t, body, "date_scheduled")
	require.Equal(t, "[]", body["file_ids"])
	var destinations []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body["destinations"].(string)), &destinations))
	require.Equal(t, map[string]any{"channel_id": "C1"}, destinations[0])
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body["blocks"].(string)), &blocks))
	require.Equal(t, "rich_text", blocks[0]["type"])
	require.Equal(t, "hello", blocks[0]["elements"].([]any)[0].(map[string]any)["elements"].([]any)[0].(map[string]any)["text"])
	firstID, ok := uuid.Parse(body["client_msg_id"].(string))
	require.NoError(t, ok)
	require.NotEqual(t, uuid.Nil, firstID)
	client = newDraftClient(t, `{"ok":true}`, &second)
	_, err = client.DraftsCreate(context.Background(), DraftInput{ChannelID: "C1", Text: "two"})
	require.NoError(t, err)
	var body2 map[string]any
	require.NoError(t, json.Unmarshal(second.body, &body2))
	require.NotEqual(t, body["client_msg_id"], body2["client_msg_id"])
}

func TestUnitDraftsUpdateAndList(t *testing.T) {
	var update capturedDraftRequest
	client := newDraftClient(t, `{"ok":true,"draft":{"id":"D2","last_updated_ts":"2"}}`, &update)
	got, err := client.DraftsUpdate(context.Background(), "D2", "1700000000.1", DraftInput{ChannelID: "C1", Text: "updated", DateScheduled: "ignored"})
	require.NoError(t, err)
	require.Equal(t, "D2", got.ID)
	require.Equal(t, "/api/drafts.update", update.path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(update.body, &body))
	require.Equal(t, "D2", body["draft_id"])
	require.Equal(t, "1700000000.1000000", body["client_last_updated_ts"])
	require.NotContains(t, body, "is_from_composer")
	require.NotContains(t, body, "date_scheduled")
	var listed capturedDraftRequest
	client = newDraftClient(t, `{"ok":true,"drafts":[{"id":"D1","date_created":1700}]}`, &listed)
	active := false
	result, err := client.DraftsList(context.Background(), &active, 25)
	require.NoError(t, err)
	require.Len(t, result.Drafts, 1)
	require.Equal(t, "/api/drafts.list", listed.path)
	require.Equal(t, "false", mustJSONField(t, listed.body, "is_active"))
	require.Equal(t, "25", mustJSONField(t, listed.body, "limit"))
}

func mustJSONField(t *testing.T, data []byte, key string) string {
	var body map[string]string
	require.NoError(t, json.Unmarshal(data, &body))
	return body[key]
}

func TestUnitDraftsDeleteFormAndSlackError(t *testing.T) {
	var captured capturedDraftRequest
	client := newDraftClient(t, `{"ok":false,"error":"draft_not_found"}`, &captured)
	err := client.DraftsDelete(context.Background(), "D1", "1700000000.1")
	require.ErrorContains(t, err, "draft_not_found")
	require.Equal(t, "/api/drafts.delete", captured.path)
	require.Equal(t, "1700000000.1000000", captured.form.Get("client_last_updated_ts"))
	require.Equal(t, "D1", captured.form.Get("draft_id"))
	require.Equal(t, "token", captured.form.Get("token"))
}
