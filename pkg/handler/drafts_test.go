package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/c3-oss/slack-mcp-server/pkg/provider/edge"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeDraftsAPI struct {
	listResponse edge.DraftsListResponse
	err          error

	listCalls   int
	listActive  *bool
	listLimit   int
	createCalls []edge.DraftInput
	updateCalls []fakeDraftUpdate
	deleteCalls []fakeDraftDelete
}

type fakeDraftUpdate struct {
	draftID, timestamp string
	input              edge.DraftInput
}

type fakeDraftDelete struct {
	draftID, timestamp string
}

func (f *fakeDraftsAPI) DraftsList(_ context.Context, isActive *bool, limit int) (edge.DraftsListResponse, error) {
	f.listCalls++
	f.listActive, f.listLimit = isActive, limit
	return f.listResponse, f.err
}

func (f *fakeDraftsAPI) DraftsCreate(_ context.Context, input edge.DraftInput) (edge.Draft, error) {
	f.createCalls = append(f.createCalls, input)
	return edge.Draft{ID: "Dr1", LastUpdatedTs: "1700000000.0000001", Destinations: []edge.DraftDestination{{ChannelID: input.ChannelID}}}, f.err
}

func (f *fakeDraftsAPI) DraftsUpdate(_ context.Context, draftID, timestamp string, input edge.DraftInput) (edge.Draft, error) {
	f.updateCalls = append(f.updateCalls, fakeDraftUpdate{draftID: draftID, timestamp: timestamp, input: input})
	return edge.Draft{ID: draftID, LastUpdatedTs: "1700000000.0000002", Destinations: []edge.DraftDestination{{ChannelID: input.ChannelID}}}, f.err
}

func (f *fakeDraftsAPI) DraftsDelete(_ context.Context, draftID, timestamp string) error {
	f.deleteCalls = append(f.deleteCalls, fakeDraftDelete{draftID: draftID, timestamp: timestamp})
	return f.err
}

func newTestDraftsHandler(api *fakeDraftsAPI) *DraftsHandler {
	return &DraftsHandler{
		slack:  func() draftsAPI { return api },
		logger: zap.NewNop(),
		channelName: func(channelID string) string {
			if channelID == "C1" {
				return "#general"
			}
			return channelID
		},
		resolveChannel: func(_ context.Context, channel string) (string, error) {
			switch channel {
			case "#general":
				return "C1", nil
			case "#unknown":
				return "", errors.New("channel \"#unknown\" not found")
			}
			return channel, nil
		},
	}
}

func draftsRequest(args map[string]any) mcp.CallToolRequest {
	var request mcp.CallToolRequest
	request.Params.Arguments = args
	return request
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func clearDraftsEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SLACK_MCP_DRAFTS_TOOL", "")
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")
}

func TestUnitDraftTextExtractsRichText(t *testing.T) {
	var raw json.RawMessage = []byte(`[ {"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hello "},{"type":"text","text":"world"}]}]} ]`)
	assert.Equal(t, "hello world", draftText(raw))
}

func TestUnitDraftTextToleratesUnexpectedBlocks(t *testing.T) {
	assert.Equal(t, "", draftText(json.RawMessage(`{"unexpected":true}`)))
	assert.Equal(t, "", draftText(json.RawMessage(`not-json`)))
	assert.Equal(t, "", draftText(nil))
}

func TestUnitDraftsListHandler(t *testing.T) {
	clearDraftsEnv(t)
	api := &fakeDraftsAPI{listResponse: edge.DraftsListResponse{Drafts: []edge.Draft{
		{
			ID:            "Dr1",
			LastUpdatedTs: "1700000000.1234567",
			Destinations:  []edge.DraftDestination{{ChannelID: "C1", ThreadTS: "1.2", Broadcast: true}},
			Blocks:        json.RawMessage(`[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"hi"}]}]}]`),
			FileIDs:       []string{"F1", "F2"},
			DateScheduled: "1700000100",
			DateCreated:   "1700000000",
		},
		{ID: "Dr2", Destinations: []edge.DraftDestination{{ChannelID: "C9"}}},
	}}}
	handler := newTestDraftsHandler(api)

	t.Run("forwards filters and renders CSV", func(t *testing.T) {
		result, err := handler.DraftsListHandler(context.Background(), draftsRequest(map[string]any{"is_active": true, "limit": float64(5)}))
		require.NoError(t, err)
		require.NotNil(t, api.listActive)
		assert.True(t, *api.listActive)
		assert.Equal(t, 5, api.listLimit)
		assert.Equal(t,
			"DraftID,ChannelID,ChannelName,ThreadTs,Broadcast,Text,FileIDs,LastUpdatedTs,DateScheduled,DateCreated,IsSent\n"+
				"Dr1,C1,#general,1.2,true,hi,\"F1,F2\",1700000000.1234567,1700000100,1700000000,false\n"+
				"Dr2,C9,C9,,false,,,,,,false\n",
			resultText(t, result))
	})

	t.Run("unset filters are not forwarded", func(t *testing.T) {
		_, err := handler.DraftsListHandler(context.Background(), draftsRequest(map[string]any{}))
		require.NoError(t, err)
		assert.Nil(t, api.listActive)
		assert.Equal(t, 0, api.listLimit)
	})

	t.Run("slack error is surfaced", func(t *testing.T) {
		failing := newTestDraftsHandler(&fakeDraftsAPI{err: errors.New("invalid_auth")})
		_, err := failing.DraftsListHandler(context.Background(), draftsRequest(map[string]any{}))
		assert.ErrorContains(t, err, "invalid_auth")
	})
}

func TestUnitDraftsCreateHandler(t *testing.T) {
	baseArgs := func() map[string]any {
		return map[string]any{"channel_id": "C1", "text": "hello"}
	}

	t.Run("maps input and returns the created draft", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		api := &fakeDraftsAPI{}
		args := baseArgs()
		args["channel_id"] = "#general"
		args["thread_ts"] = "1.2"
		args["broadcast"] = true
		args["file_ids"] = " F1, ,F2 "
		result, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(args))
		require.NoError(t, err)
		require.Len(t, api.createCalls, 1)
		assert.Equal(t, edge.DraftInput{ChannelID: "C1", Text: "hello", ThreadTS: "1.2", Broadcast: true, FileIDs: []string{"F1", "F2"}}, api.createCalls[0])
		assert.Contains(t, resultText(t, result), "Dr1,C1,#general")
	})

	t.Run("allowlist of IDs accepts a channel given by name", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "C1")
		api := &fakeDraftsAPI{}
		args := baseArgs()
		args["channel_id"] = "#general"
		_, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(args))
		require.NoError(t, err)
		assert.Len(t, api.createCalls, 1)
	})

	rejected := []struct {
		name        string
		env         map[string]string
		args        map[string]any
		errContains string
	}{
		{name: "disabled by default", env: map[string]string{}, args: baseArgs(), errContains: "drafts_create tool is disabled"},
		{name: "only drafts_list enabled", env: map[string]string{"SLACK_MCP_ENABLED_TOOLS": "drafts_list"}, args: baseArgs(), errContains: "drafts_create tool is disabled"},
		{name: "channel outside allowlist", env: map[string]string{"SLACK_MCP_DRAFTS_TOOL": "C2"}, args: baseArgs(), errContains: "not allowed for channel \"C1\""},
		{name: "channel in denylist", env: map[string]string{"SLACK_MCP_DRAFTS_TOOL": "!C1"}, args: baseArgs(), errContains: "not allowed for channel \"C1\""},
		{name: "missing text", env: map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"}, args: map[string]any{"channel_id": "C1"}, errContains: "required"},
		{name: "unknown channel name", env: map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"}, args: map[string]any{"channel_id": "#unknown", "text": "x"}, errContains: "not found"},
		{
			name:        "scheduling without add-message permission",
			env:         map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"},
			args:        map[string]any{"channel_id": "C1", "text": "x", "date_scheduled": "1800000000"},
			errContains: "SLACK_MCP_ADD_MESSAGE_TOOL",
		},
		{
			name:        "scheduling to a channel outside the add-message allowlist",
			env:         map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true", "SLACK_MCP_ADD_MESSAGE_TOOL": "C2"},
			args:        map[string]any{"channel_id": "C1", "text": "x", "date_scheduled": "1800000000"},
			errContains: "SLACK_MCP_ADD_MESSAGE_TOOL",
		},
	}
	for _, tc := range rejected {
		t.Run("rejects: "+tc.name, func(t *testing.T) {
			clearDraftsEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			api := &fakeDraftsAPI{}
			_, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(tc.args))
			assert.ErrorContains(t, err, tc.errContains)
			assert.Empty(t, api.createCalls)
			assert.Zero(t, api.listCalls)
		})
	}

	t.Run("enabled only via enabled tools list", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "drafts_list, drafts_create")
		api := &fakeDraftsAPI{}
		_, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(baseArgs()))
		require.NoError(t, err)
		assert.Len(t, api.createCalls, 1)
	})

	t.Run("scheduling allowed when add-message permits the channel", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")
		api := &fakeDraftsAPI{}
		args := baseArgs()
		args["date_scheduled"] = "1800000000"
		_, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(args))
		require.NoError(t, err)
		require.Len(t, api.createCalls, 1)
		assert.Equal(t, "1800000000", api.createCalls[0].DateScheduled)
	})

	t.Run("slack error is surfaced", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		api := &fakeDraftsAPI{err: errors.New("channel_not_found")}
		_, err := newTestDraftsHandler(api).DraftsCreateHandler(context.Background(), draftsRequest(baseArgs()))
		assert.ErrorContains(t, err, "channel_not_found")
	})
}

func TestUnitDraftsUpdateHandler(t *testing.T) {
	baseArgs := func() map[string]any {
		return map[string]any{"draft_id": "Dr1", "client_last_updated_ts": "1700000000.1", "channel_id": "C1", "text": "edited"}
	}

	t.Run("maps input and returns the updated draft", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "C1")
		api := &fakeDraftsAPI{listResponse: edge.DraftsListResponse{Drafts: []edge.Draft{
			{ID: "Dr1", LastUpdatedTs: "1700000000.1", Destinations: []edge.DraftDestination{{ChannelID: "C1"}}},
		}}}
		args := baseArgs()
		args["file_ids"] = "F1"
		result, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(args))
		require.NoError(t, err)
		require.Len(t, api.updateCalls, 1)
		assert.Equal(t, fakeDraftUpdate{
			draftID:   "Dr1",
			timestamp: "1700000000.1",
			input:     edge.DraftInput{ChannelID: "C1", Text: "edited", FileIDs: []string{"F1"}},
		}, api.updateCalls[0])
		assert.Contains(t, resultText(t, result), "Dr1,C1,#general")
	})

	for _, missing := range []string{"draft_id", "client_last_updated_ts", "channel_id", "text"} {
		t.Run("rejects missing "+missing, func(t *testing.T) {
			clearDraftsEnv(t)
			t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
			api := &fakeDraftsAPI{}
			args := baseArgs()
			delete(args, missing)
			_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(args))
			assert.ErrorContains(t, err, "required")
			assert.Empty(t, api.updateCalls)
		})
	}

	t.Run("rejects channel outside allowlist", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "C2")
		api := &fakeDraftsAPI{}
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(baseArgs()))
		assert.ErrorContains(t, err, "not allowed")
		assert.Empty(t, api.updateCalls)
	})

	t.Run("rejects when disabled", func(t *testing.T) {
		clearDraftsEnv(t)
		api := &fakeDraftsAPI{}
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(baseArgs()))
		assert.ErrorContains(t, err, "drafts_update tool is disabled")
		assert.Empty(t, api.updateCalls)
	})

	scheduledDraft := edge.DraftsListResponse{Drafts: []edge.Draft{{
		ID:            "Dr1",
		LastUpdatedTs: "1700000000.1",
		Destinations:  []edge.DraftDestination{{ChannelID: "C1"}},
		DateScheduled: "1800000000",
	}}}

	t.Run("rejects retargeting a scheduled draft to a channel add-message forbids", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")
		api := &fakeDraftsAPI{listResponse: scheduledDraft}
		args := baseArgs()
		args["channel_id"] = "C2"
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(args))
		assert.ErrorContains(t, err, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.Empty(t, api.updateCalls)
	})

	t.Run("allows editing a scheduled draft in a channel add-message permits", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")
		api := &fakeDraftsAPI{listResponse: scheduledDraft}
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(baseArgs()))
		require.NoError(t, err)
		assert.Len(t, api.updateCalls, 1)
	})

	t.Run("rejects editing a draft whose current destination the policy forbids", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "!C1")
		api := &fakeDraftsAPI{listResponse: scheduledDraft}
		args := baseArgs()
		args["channel_id"] = "C2"
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(args))
		assert.ErrorContains(t, err, "not allowed for channel \"C1\"")
		assert.Empty(t, api.updateCalls)
	})

	t.Run("rejects unknown draft", func(t *testing.T) {
		clearDraftsEnv(t)
		t.Setenv("SLACK_MCP_DRAFTS_TOOL", "true")
		api := &fakeDraftsAPI{}
		_, err := newTestDraftsHandler(api).DraftsUpdateHandler(context.Background(), draftsRequest(baseArgs()))
		assert.ErrorContains(t, err, "draft_not_found")
		assert.Empty(t, api.updateCalls)
	})
}

func TestUnitDraftsDeleteHandler(t *testing.T) {
	listed := edge.DraftsListResponse{Drafts: []edge.Draft{
		{ID: "Dr1", LastUpdatedTs: "1700000000.1234567", Destinations: []edge.DraftDestination{{ChannelID: "C1"}}},
		{ID: "Dr2", LastUpdatedTs: "1700000000.7654321"},
		{ID: "Dr3", LastUpdatedTs: "1700000000.3", Destinations: []edge.DraftDestination{{ChannelID: "C1"}, {ChannelID: "C2"}}},
	}}

	tests := []struct {
		name          string
		env           map[string]string
		args          map[string]any
		errContains   string
		wantListCalls int
		wantDelete    []fakeDraftDelete
	}{
		{
			name:       "allow-all policy with timestamp skips lookup",
			env:        map[string]string{"SLACK_MCP_DRAFTS_TOOL": "yes"},
			args:       map[string]any{"draft_id": "Dr1", "client_last_updated_ts": "1.5"},
			wantDelete: []fakeDraftDelete{{draftID: "Dr1", timestamp: "1.5"}},
		},
		{
			name:          "timestamp omitted is looked up",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"},
			args:          map[string]any{"draft_id": "Dr1"},
			wantListCalls: 1,
			wantDelete:    []fakeDraftDelete{{draftID: "Dr1", timestamp: "1700000000.1234567"}},
		},
		{
			name:          "restricted policy looks up destination even with timestamp",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "C1"},
			args:          map[string]any{"draft_id": "Dr1", "client_last_updated_ts": "1.5"},
			wantListCalls: 1,
			wantDelete:    []fakeDraftDelete{{draftID: "Dr1", timestamp: "1.5"}},
		},
		{
			name:          "restricted policy denies other channel",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "!C1"},
			args:          map[string]any{"draft_id": "Dr1", "client_last_updated_ts": "1.5"},
			errContains:   "not allowed for channel \"C1\"",
			wantListCalls: 1,
		},
		{
			name:          "restricted policy fails closed on unknown destination",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "!C9"},
			args:          map[string]any{"draft_id": "Dr2"},
			errContains:   "destination channel is unknown",
			wantListCalls: 1,
		},
		{
			name:          "restricted policy checks every destination",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "C1"},
			args:          map[string]any{"draft_id": "Dr3"},
			errContains:   "not allowed for channel \"C2\"",
			wantListCalls: 1,
		},
		{
			name:          "draft not found",
			env:           map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"},
			args:          map[string]any{"draft_id": "missing"},
			errContains:   "draft_not_found",
			wantListCalls: 1,
		},
		{
			name:        "disabled by default",
			env:         map[string]string{},
			args:        map[string]any{"draft_id": "Dr1"},
			errContains: "drafts_delete tool is disabled",
		},
		{
			name:        "missing draft id",
			env:         map[string]string{"SLACK_MCP_DRAFTS_TOOL": "true"},
			args:        map[string]any{},
			errContains: "draft_id is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearDraftsEnv(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			api := &fakeDraftsAPI{listResponse: listed}
			result, err := newTestDraftsHandler(api).DraftsDeleteHandler(context.Background(), draftsRequest(tc.args))
			if tc.errContains != "" {
				assert.ErrorContains(t, err, tc.errContains)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "draft deleted", resultText(t, result))
			}
			assert.Equal(t, tc.wantListCalls, api.listCalls)
			assert.Equal(t, tc.wantDelete, api.deleteCalls)
		})
	}
}
