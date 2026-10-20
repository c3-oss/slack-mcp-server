package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/c3-oss/slack-mcp-server/pkg/provider"
	"github.com/c3-oss/slack-mcp-server/pkg/provider/edge"
	"github.com/gocarina/gocsv"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

const (
	draftsToolEnv     = "SLACK_MCP_DRAFTS_TOOL"
	addMessageToolEnv = "SLACK_MCP_ADD_MESSAGE_TOOL"
	enabledToolsEnv   = "SLACK_MCP_ENABLED_TOOLS"

	draftsCreateTool      = "drafts_create"
	draftsUpdateTool      = "drafts_update"
	draftsDeleteTool      = "drafts_delete"
	addMessageToolForGate = "conversations_add_message"
)

// DraftRow is the CSV representation returned by the draft tools.
type DraftRow struct {
	DraftID       string `csv:"DraftID"`
	ChannelID     string `csv:"ChannelID"`
	ChannelName   string `csv:"ChannelName"`
	ThreadTs      string `csv:"ThreadTs"`
	Broadcast     bool   `csv:"Broadcast"`
	Text          string `csv:"Text"`
	FileIDs       string `csv:"FileIDs"`
	LastUpdatedTs string `csv:"LastUpdatedTs"`
	DateScheduled string `csv:"DateScheduled"`
	DateCreated   string `csv:"DateCreated"`
	IsSent        bool   `csv:"IsSent"`
}

// draftsAPI is the subset of the Slack client used by the draft tools.
type draftsAPI interface {
	DraftsList(ctx context.Context, isActive *bool, limit int) (edge.DraftsListResponse, error)
	DraftsCreate(ctx context.Context, input edge.DraftInput) (edge.Draft, error)
	DraftsUpdate(ctx context.Context, draftID, clientLastUpdatedTs string, input edge.DraftInput) (edge.Draft, error)
	DraftsDelete(ctx context.Context, draftID, clientLastUpdatedTs string) error
}

// DraftsHandler implements the drafts_* MCP tools on top of Slack's
// session-only drafts.* webclient endpoints.
type DraftsHandler struct {
	slack          func() draftsAPI
	logger         *zap.Logger
	channelName    func(channelID string) string
	resolveChannel func(ctx context.Context, channel string) (string, error)
}

// NewDraftsHandler wires the draft tools to the API provider and reuses the
// conversations handler for #channel / @user resolution.
func NewDraftsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *DraftsHandler {
	return &DraftsHandler{
		slack:  func() draftsAPI { return apiProvider.Slack() },
		logger: logger,
		channelName: func(channelID string) string {
			if channelsMaps := apiProvider.ProvideChannelsMaps(); channelsMaps != nil {
				if cached, ok := channelsMaps.Channels[channelID]; ok {
					return cached.Name
				}
			}
			return channelID
		},
		resolveChannel: convHandler.resolveChannelID,
	}
}

// DraftsListHandler lists the user's drafts as CSV.
func (h *DraftsHandler) DraftsListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("DraftsListHandler called", zap.Any("params", request.Params))

	var isActive *bool
	if value, ok := request.GetArguments()["is_active"].(bool); ok {
		isActive = &value
	}

	response, err := h.slack().DraftsList(ctx, isActive, request.GetInt("limit", 0))
	if err != nil {
		h.logger.Error("DraftsList failed", zap.Error(err))
		return nil, fmt.Errorf("failed to list drafts: %w", err)
	}

	rows := make([]DraftRow, 0, len(response.Drafts))
	for _, draft := range response.Drafts {
		rows = append(rows, h.draftRow(draft))
	}
	return marshalDraftRows(rows)
}

// DraftsCreateHandler creates a draft addressed to a channel or thread.
func (h *DraftsHandler) DraftsCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("DraftsCreateHandler called", zap.Any("params", request.Params))

	policy, err := writeToolPolicy(draftsCreateTool, draftsToolEnv)
	if err != nil {
		return nil, err
	}
	channel, text := request.GetString("channel_id", ""), request.GetString("text", "")
	if channel == "" || text == "" {
		return nil, errors.New("channel_id and text are required parameters")
	}
	channelID, err := h.resolveAllowedChannel(ctx, draftsCreateTool, policy, channel)
	if err != nil {
		return nil, err
	}

	input := draftInputFromRequest(request, channelID, text)
	input.DateScheduled = request.GetString("date_scheduled", "")
	if input.DateScheduled != "" {
		if err := checkSchedulingAllowed(channelID); err != nil {
			return nil, err
		}
	}

	draft, err := h.slack().DraftsCreate(ctx, input)
	if err != nil {
		h.logger.Error("DraftsCreate failed", zap.Error(err))
		return nil, fmt.Errorf("failed to create draft: %w", err)
	}
	return marshalDraftRows([]DraftRow{h.draftRow(draft)})
}

// DraftsUpdateHandler replaces the text and destination of an existing draft.
func (h *DraftsHandler) DraftsUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("DraftsUpdateHandler called", zap.Any("params", request.Params))

	policy, err := writeToolPolicy(draftsUpdateTool, draftsToolEnv)
	if err != nil {
		return nil, err
	}
	draftID := request.GetString("draft_id", "")
	lastUpdatedTs := request.GetString("client_last_updated_ts", "")
	channel, text := request.GetString("channel_id", ""), request.GetString("text", "")
	if draftID == "" || lastUpdatedTs == "" || channel == "" || text == "" {
		return nil, errors.New("draft_id, client_last_updated_ts, channel_id and text are required parameters")
	}
	channelID, err := h.resolveAllowedChannel(ctx, draftsUpdateTool, policy, channel)
	if err != nil {
		return nil, err
	}

	draft, err := h.slack().DraftsUpdate(ctx, draftID, lastUpdatedTs, draftInputFromRequest(request, channelID, text))
	if err != nil {
		h.logger.Error("DraftsUpdate failed", zap.Error(err))
		return nil, fmt.Errorf("failed to update draft: %w", err)
	}
	return marshalDraftRows([]DraftRow{h.draftRow(draft)})
}

// DraftsDeleteHandler deletes a draft.
//
// With an allow-all policy and an explicit client_last_updated_ts the draft is
// deleted directly. Otherwise the draft is looked up via drafts.list to fill in
// the timestamp and to check its destination channel against the policy; a
// draft that cannot be found, or whose destination is unknown under a
// restricted policy, is never deleted.
func (h *DraftsHandler) DraftsDeleteHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("DraftsDeleteHandler called", zap.Any("params", request.Params))

	policy, err := writeToolPolicy(draftsDeleteTool, draftsToolEnv)
	if err != nil {
		return nil, err
	}
	draftID := request.GetString("draft_id", "")
	if draftID == "" {
		return nil, errors.New("draft_id is required")
	}

	lastUpdatedTs := request.GetString("client_last_updated_ts", "")
	if lastUpdatedTs == "" || !policyAllowsAll(policy) {
		draft, err := h.findDraft(ctx, draftID)
		if err != nil {
			return nil, err
		}
		if !policyAllowsAll(policy) {
			channelID := draftChannelID(draft)
			if channelID == "" {
				return nil, fmt.Errorf("%s refused: the draft's destination channel is unknown, applied policy: %s", draftsDeleteTool, policy)
			}
			if !isChannelAllowedForConfig(channelID, policy) {
				return nil, channelNotAllowedError(draftsDeleteTool, channelID, policy)
			}
		}
		if lastUpdatedTs == "" {
			lastUpdatedTs = draft.LastUpdatedTs
		}
	}

	if err := h.slack().DraftsDelete(ctx, draftID, lastUpdatedTs); err != nil {
		h.logger.Error("DraftsDelete failed", zap.Error(err))
		return nil, fmt.Errorf("failed to delete draft: %w", err)
	}
	return mcp.NewToolResultText("draft deleted"), nil
}

func (h *DraftsHandler) findDraft(ctx context.Context, draftID string) (edge.Draft, error) {
	response, err := h.slack().DraftsList(ctx, nil, 0)
	if err != nil {
		h.logger.Error("DraftsList failed", zap.Error(err))
		return edge.Draft{}, fmt.Errorf("failed to look up draft: %w", err)
	}
	for _, draft := range response.Drafts {
		if draft.ID == draftID && draft.LastUpdatedTs != "" {
			return draft, nil
		}
	}
	return edge.Draft{}, errors.New("draft_not_found")
}

// resolveAllowedChannel resolves #channel / @user references to an ID and
// checks the resulting ID against the tool's channel policy.
func (h *DraftsHandler) resolveAllowedChannel(ctx context.Context, toolName, policy, channel string) (string, error) {
	channelID, err := h.resolveChannel(ctx, channel)
	if err != nil {
		return "", err
	}
	if !policyAllowsAll(policy) && !isChannelAllowedForConfig(channelID, policy) {
		return "", channelNotAllowedError(toolName, channelID, policy)
	}
	return channelID, nil
}

func (h *DraftsHandler) draftRow(draft edge.Draft) DraftRow {
	row := DraftRow{
		DraftID:       draft.ID,
		Text:          draftText(draft.Blocks),
		FileIDs:       strings.Join(draft.FileIDs, ","),
		LastUpdatedTs: draft.LastUpdatedTs,
		DateScheduled: draft.DateScheduled.String(),
		DateCreated:   draft.DateCreated.String(),
		IsSent:        draft.IsSent,
	}
	if len(draft.Destinations) > 0 {
		destination := draft.Destinations[0]
		row.ChannelID = destination.ChannelID
		row.ChannelName = h.channelName(destination.ChannelID)
		row.ThreadTs = destination.ThreadTS
		row.Broadcast = destination.Broadcast
	}
	return row
}

func draftChannelID(draft edge.Draft) string {
	if len(draft.Destinations) == 0 {
		return ""
	}
	return draft.Destinations[0].ChannelID
}

func draftInputFromRequest(request mcp.CallToolRequest, channelID, text string) edge.DraftInput {
	var fileIDs []string
	for _, fileID := range strings.Split(request.GetString("file_ids", ""), ",") {
		if trimmed := strings.TrimSpace(fileID); trimmed != "" {
			fileIDs = append(fileIDs, trimmed)
		}
	}
	return edge.DraftInput{
		ChannelID: channelID,
		Text:      text,
		ThreadTS:  request.GetString("thread_ts", ""),
		Broadcast: request.GetBool("broadcast", false),
		FileIDs:   fileIDs,
	}
}

// draftText extracts the plain text of rich_text blocks, ignoring any element
// it does not understand.
func draftText(raw json.RawMessage) string {
	var blocks []struct {
		Elements []struct {
			Elements []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"elements"`
		} `json:"elements"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var text strings.Builder
	for _, block := range blocks {
		for _, section := range block.Elements {
			for _, element := range section.Elements {
				if element.Type == "text" {
					text.WriteString(element.Text)
				}
			}
		}
	}
	return text.String()
}

func marshalDraftRows(rows []DraftRow) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(&rows)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(csvBytes)), nil
}

// writeToolPolicy returns the channel policy configured for a gated write tool.
// An empty env var disables the tool unless it is listed by exact name in
// SLACK_MCP_ENABLED_TOOLS, in which case every channel is allowed.
func writeToolPolicy(toolName, envVar string) (string, error) {
	if policy := os.Getenv(envVar); policy != "" {
		return policy, nil
	}
	for _, enabledTool := range strings.Split(os.Getenv(enabledToolsEnv), ",") {
		if strings.TrimSpace(enabledTool) == toolName {
			return "true", nil
		}
	}
	return "", fmt.Errorf(
		"by default, the %s tool is disabled to prevent accidental changes. "+
			"Set %s to true, a comma-separated channel allowlist, or a !-prefixed denylist to enable it",
		toolName, envVar)
}

// checkSchedulingAllowed enforces that a scheduled draft, which Slack sends
// automatically at the scheduled time, only targets channels where posting
// is allowed by the add-message policy.
func checkSchedulingAllowed(channelID string) error {
	policy, err := writeToolPolicy(addMessageToolForGate, addMessageToolEnv)
	if err == nil && (policyAllowsAll(policy) || isChannelAllowedForConfig(channelID, policy)) {
		return nil
	}
	return fmt.Errorf("scheduled drafts are sent by Slack at the scheduled time, so channel %q must also be allowed by %s", channelID, addMessageToolEnv)
}

func policyAllowsAll(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func channelNotAllowedError(toolName, channelID, policy string) error {
	return fmt.Errorf("%s is not allowed for channel %q, applied policy: %s", toolName, channelID, policy)
}
