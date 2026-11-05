package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/c3-oss/slack-mcp-server/pkg/limiter"
	"github.com/c3-oss/slack-mcp-server/pkg/provider"
	"github.com/c3-oss/slack-mcp-server/pkg/provider/edge"
	"github.com/gocarina/gocsv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type SavedItemRow struct {
	ItemID      string `csv:"ItemID"`
	ItemType    string `csv:"ItemType"`
	ItemDetail  string `csv:"ItemDetail"`
	ChannelID   string `csv:"ChannelID"`
	ChannelName string `csv:"ChannelName"`
	Ts          string `csv:"Ts"`
	DateCreated string `csv:"DateCreated"`
	DateDue     string `csv:"DateDue"`
	State       string `csv:"State"`
	TodoState   string `csv:"TodoState"`
	Text        string `csv:"Text"`
	UserID      string `csv:"UserID"`
	UserName    string `csv:"UserName"`
}

type SavedHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
	convHandler *ConversationsHandler
}

func NewSavedHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *SavedHandler {
	return &SavedHandler{apiProvider: apiProvider, logger: logger, convHandler: convHandler}
}

func (h *SavedHandler) SavedListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("SavedListHandler called", zap.Any("params", request.Params))

	view, apiFilters, err := savedListFilters(request.GetString("filter", "all"))
	if err != nil {
		return nil, err
	}
	limit := request.GetInt("limit", 50)
	if limit < 1 {
		limit = 50
	}
	includeMessages := request.GetBool("include_messages", true)
	maxMsgsPerItem := request.GetInt("max_messages_per_item", 5)
	if maxMsgsPerItem < 1 {
		maxMsgsPerItem = 1
	}

	channelsMaps := h.apiProvider.ProvideChannelsMaps()
	var channels map[string]provider.Channel
	if channelsMaps != nil {
		channels = channelsMaps.Channels
	}
	rl := limiter.Tier3.Limiter()

	byFilter := map[string][]savedListEntry{}
	var counts edge.SavedCounts
	seen := map[string]bool{}
	countsSet := false
	firstCall := true
	stop := false

	for _, apiFilter := range apiFilters {
		if stop {
			break
		}
		cursor := ""
		for len(byFilter[apiFilter]) < limit && !stop {
			if !firstCall {
				if err := rl.Wait(ctx); err != nil {
					h.logger.Warn("Rate limiter wait failed, stopping pagination", zap.Error(err))
					stop = true
					break
				}
			}
			firstCall = false

			resp, err := h.apiProvider.Slack().SavedList(ctx, apiFilter, savedListPageSize, cursor)
			if err != nil {
				h.logger.Error("SavedList failed", zap.String("filter", apiFilter), zap.Error(err))
				return nil, fmt.Errorf("failed to list saved items: %v", err)
			}
			if !countsSet {
				counts = resp.Counts
				countsSet = true
			}

			for _, item := range resp.SavedItems {
				if len(byFilter[apiFilter]) >= limit {
					break
				}
				if !savedItemMatches(item, view) {
					continue
				}
				key := item.ItemType + "|" + item.ItemID + "|" + item.ItemDetail + "|" + item.Ts
				if seen[key] {
					continue
				}
				seen[key] = true
				byFilter[apiFilter] = append(byFilter[apiFilter], savedListEntry{item: item, row: savedRow(item, channels)})
			}

			if resp.ResponseMetadata.NextCursor == "" || len(byFilter[apiFilter]) >= limit {
				break
			}
			cursor = resp.ResponseMetadata.NextCursor
		}
	}

	entries := mergeSavedEntries(view, byFilter, limit)
	rows := make([]SavedItemRow, 0, len(entries))
	for _, entry := range entries {
		row := entry.row
		if includeMessages && savedItemIsMessage(entry.item) && !stop {
			if err := rl.Wait(ctx); err != nil {
				h.logger.Warn("Rate limiter wait failed, stopping message fetch", zap.Error(err))
				stop = true
			} else {
				text, userID, userName, lookupErr := h.lookupSavedMessage(ctx, entry.item, maxMsgsPerItem)
				if lookupErr != nil {
					h.logger.Warn("Failed to fetch saved message",
						zap.String("channel", entry.item.ItemID),
						zap.String("ts", entry.item.Ts),
						zap.Error(lookupErr))
					text = "[message content unavailable]"
				}
				row.Text = text
				row.UserID = userID
				row.UserName = userName
			}
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		return mcp.NewToolResultText(formatSavedCounts(view, counts)), nil
	}

	csvBytes, err := gocsv.MarshalBytes(&rows)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal saved items: %v", err)
	}
	return mcp.NewToolResultText(string(csvBytes)), nil
}

func (h *SavedHandler) SavedUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("SavedUpdateHandler called", zap.Any("params", request.Params))

	itemID := request.GetString("item_id", "")
	ts := request.GetString("ts", "")
	mark := request.GetString("mark", "")
	dateDue := int64(request.GetInt("date_due", 0))

	if itemID == "" || ts == "" {
		return nil, fmt.Errorf("item_id and ts are required parameters")
	}
	if mark == "" && dateDue == 0 {
		return nil, fmt.Errorf("at least one of mark or date_due must be provided")
	}

	err := h.apiProvider.Slack().SavedUpdate(ctx, "message", itemID, ts, mark, dateDue)
	if err != nil {
		h.logger.Error("SavedUpdate failed",
			zap.String("item_id", itemID),
			zap.String("ts", ts),
			zap.String("mark", mark),
			zap.Int64("date_due", dateDue),
			zap.Error(err))
		return nil, fmt.Errorf("failed to update saved item: %v", err)
	}

	action := "updated"
	if mark == "completed" {
		action = "marked as completed"
	}
	if dateDue > 0 {
		dueTime := time.Unix(dateDue, 0).UTC().Format("2006-01-02 15:04")
		action += fmt.Sprintf(", due date set to %s", dueTime)
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully %s saved item (item_id=%s, ts=%s)", action, itemID, ts)), nil
}

func (h *SavedHandler) SavedClearCompletedHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("SavedClearCompletedHandler called", zap.Any("params", request.Params))

	err := h.apiProvider.Slack().SavedClearCompleted(ctx)
	if err != nil {
		h.logger.Error("SavedClearCompleted failed", zap.Error(err))
		return nil, fmt.Errorf("failed to clear completed saved items: %v", err)
	}

	return mcp.NewToolResultText("Successfully cleared all completed saved items"), nil
}

const savedListPageSize = 50

type savedListEntry struct {
	item edge.SavedItem
	row  SavedItemRow
}

// mergeSavedEntries keeps both tabs in an "all" result when they do not both fit in limit.
func mergeSavedEntries(view string, byFilter map[string][]savedListEntry, limit int) []savedListEntry {
	if view != "all" {
		for _, entries := range byFilter {
			if len(entries) > limit {
				return entries[:limit]
			}
			return entries
		}
		return nil
	}

	saved := byFilter["saved"]
	todos := byFilter["todo"]
	if len(saved)+len(todos) <= limit {
		out := make([]savedListEntry, 0, len(saved)+len(todos))
		out = append(out, todos...)
		out = append(out, saved...)
		return out
	}

	todoTake := limit / 2
	if len(todos) < todoTake {
		todoTake = len(todos)
	}
	savedTake := limit - todoTake
	if len(saved) < savedTake {
		savedTake = len(saved)
		todoTake = limit - savedTake
		if todoTake > len(todos) {
			todoTake = len(todos)
		}
	}
	out := make([]savedListEntry, 0, savedTake+todoTake)
	out = append(out, todos[:todoTake]...)
	out = append(out, saved[:savedTake]...)
	return out
}

// saved.list uses one filter per tab. "todo" is the To-dos tab.
func savedListFilters(raw string) (view string, apiFilters []string, err error) {
	switch strings.TrimSpace(raw) {
	case "", "all":
		return "all", []string{"saved", "todo"}, nil
	case "saved":
		return "saved", []string{"saved"}, nil
	case "to_do", "todo":
		return "to_do", []string{"todo"}, nil
	case "completed":
		return "completed", []string{"completed"}, nil
	case "archived":
		return "archived", []string{"archived"}, nil
	default:
		return "", nil, fmt.Errorf("filter must be one of all, saved, to_do, completed, archived")
	}
}

func savedItemView(item edge.SavedItem) string {
	if item.State == "completed" || item.DateCompleted > 0 {
		return "completed"
	}
	if item.IsArchived || item.State == "archived" {
		return "archived"
	}
	switch item.TodoState {
	case "saved", "to_do":
		return item.TodoState
	}
	if item.DateDue > 0 {
		return "to_do"
	}
	return "saved"
}

func savedItemMatches(item edge.SavedItem, view string) bool {
	got := savedItemView(item)
	if view == "all" {
		return got == "saved" || got == "to_do"
	}
	return got == view
}

func savedItemIsMessage(item edge.SavedItem) bool {
	return item.ItemType == "" || item.ItemType == "message"
}

func savedRow(item edge.SavedItem, channels map[string]provider.Channel) SavedItemRow {
	row := SavedItemRow{
		ItemID:      item.ItemID,
		ItemType:    item.ItemType,
		ItemDetail:  item.ItemDetail,
		Ts:          item.Ts,
		DateCreated: formatUnixTs(item.DateCreated),
		DateDue:     formatUnixTs(item.DateDue),
		State:       item.State,
		TodoState:   savedItemView(item),
		Text:        item.Description,
	}
	if savedItemIsMessage(item) {
		row.ChannelID = item.ItemID
		row.ChannelName = item.ItemID
		if cached, ok := channels[item.ItemID]; ok {
			row.ChannelName = cached.Name
		}
	}
	return row
}

func formatSavedCounts(view string, counts edge.SavedCounts) string {
	return fmt.Sprintf(
		"No items in view %q. saved_no_due_date_count=%d todo_count=%d todo_unfiltered_count=%d uncompleted_count=%d uncompleted_overdue_count=%d archived_count=%d completed_count=%d total_count=%d",
		view,
		counts.SavedNoDueDateCount,
		counts.TodoCount,
		counts.TodoUnfilteredCount,
		counts.UncompletedCount,
		counts.UncompletedOverdueCount,
		counts.ArchivedCount,
		counts.CompletedCount,
		counts.TotalCount,
	)
}

func (h *SavedHandler) lookupSavedMessage(ctx context.Context, item edge.SavedItem, maxMsgs int) (text, userID, userName string, err error) {
	histParams := &slack.GetConversationHistoryParameters{
		ChannelID: item.ItemID,
		Latest:    item.Ts,
		Oldest:    item.Ts,
		Inclusive: true,
		Limit:     1,
	}
	histResp, err := h.apiProvider.Slack().GetConversationHistoryContext(ctx, histParams)
	if err != nil {
		return "", "", "", err
	}

	var slackMsgs []slack.Message
	if len(histResp.Messages) > 0 {
		msg := histResp.Messages[0]
		if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
			repliesParams := &slack.GetConversationRepliesParameters{
				ChannelID: item.ItemID,
				Timestamp: msg.ThreadTimestamp,
				Latest:    item.Ts,
				Oldest:    item.Ts,
				Inclusive: true,
				Limit:     maxMsgs,
			}
			replies, _, _, repliesErr := h.apiProvider.Slack().GetConversationRepliesContext(ctx, repliesParams)
			if repliesErr == nil && len(replies) > 0 {
				slackMsgs = replies
			}
		}
		if len(slackMsgs) == 0 {
			slackMsgs = histResp.Messages
		}
	} else if item.Ts != "" {
		repliesParams := &slack.GetConversationRepliesParameters{
			ChannelID: item.ItemID,
			Timestamp: item.Ts,
			Latest:    item.Ts,
			Oldest:    item.Ts,
			Inclusive: true,
			Limit:     maxMsgs,
		}
		replies, _, _, repliesErr := h.apiProvider.Slack().GetConversationRepliesContext(ctx, repliesParams)
		if repliesErr == nil && len(replies) > 0 {
			slackMsgs = replies
		}
	}

	if len(slackMsgs) == 0 {
		if item.Description != "" {
			return item.Description, "", "", nil
		}
		return "[saved item — message not found in channel history]", "", "", nil
	}

	msgs := h.convHandler.convertMessagesFromHistory(ctx, slackMsgs, item.ItemID, false)
	if len(msgs) == 0 {
		if item.Description != "" {
			return item.Description, "", "", nil
		}
		return "[saved item — message not found in channel history]", "", "", nil
	}
	picked := msgs[0]
	for _, msg := range msgs {
		if msg.MsgID == item.Ts {
			picked = msg
			break
		}
	}
	body := picked.Text
	if body == "" {
		body = item.Description
	}
	return body, picked.UserID, picked.UserName, nil
}

func formatUnixTs(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04")
}
