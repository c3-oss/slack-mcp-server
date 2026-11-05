package handler

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/c3-oss/slack-mcp-server/pkg/provider"
	"github.com/c3-oss/slack-mcp-server/pkg/provider/edge"
	"github.com/gocarina/gocsv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitSavedItemCSVFormat(t *testing.T) {
	items := []SavedItemRow{
		{
			ItemID:      "C0AJMCRNH0U",
			ItemType:    "message",
			ChannelID:   "C0AJMCRNH0U",
			ChannelName: "#wg-feast-ui",
			Ts:          "1772680334.954409",
			DateCreated: "2026-03-03 12:00",
			State:       "in_progress",
			TodoState:   "saved",
			Text:        "please review",
		},
		{
			ItemID:      "D0AGSQXLJHG",
			ItemType:    "message",
			ChannelID:   "D0AGSQXLJHG",
			ChannelName: "@john",
			Ts:          "1771941381.234049",
			DateCreated: "2026-02-25 08:30",
			DateDue:     "2026-03-01 15:00",
			State:       "in_progress",
			TodoState:   "to_do",
			Text:        "ship the fix",
		},
	}

	csvBytes, err := gocsv.MarshalBytes(&items)
	require.NoError(t, err)
	csvStr := string(csvBytes)

	assert.Contains(t, csvStr, "ItemID,ItemType,ItemDetail,ChannelID,ChannelName,Ts,DateCreated,DateDue,State,TodoState,Text,UserID,UserName")
	assert.Contains(t, csvStr, "C0AJMCRNH0U")
	assert.Contains(t, csvStr, "#wg-feast-ui")
	assert.Contains(t, csvStr, "1772680334.954409")
	assert.Contains(t, csvStr, "in_progress")
	assert.Contains(t, csvStr, "to_do")
	assert.Contains(t, csvStr, "please review")
	assert.Contains(t, csvStr, "@john")
}

func TestUnitSavedItemFieldExtraction(t *testing.T) {
	item := edge.SavedItem{
		ItemID:           "C092WJP9Z38",
		ItemType:         "message",
		DateCreated:      1772036567,
		DateDue:          1772521200,
		DateCompleted:    0,
		DateUpdated:      1772036567,
		IsArchived:       false,
		DateSnoozedUntil: 0,
		Ts:               "1772034406.593509",
		State:            "in_progress",
		TodoState:        "to_do",
	}

	assert.Equal(t, "C092WJP9Z38", item.ItemID)
	assert.Equal(t, "message", item.ItemType)
	assert.Equal(t, "in_progress", item.State)
	assert.Equal(t, "to_do", item.TodoState)
	assert.Equal(t, int64(1772521200), item.DateDue)
	assert.Equal(t, int64(0), item.DateCompleted)
	assert.Equal(t, "1772034406.593509", item.Ts)
	assert.False(t, item.IsArchived)
}

func TestUnitSavedCountsDeserialization(t *testing.T) {
	counts := edge.SavedCounts{
		UncompletedCount:        51,
		UncompletedOverdueCount: 1,
		ArchivedCount:           0,
		CompletedCount:          0,
		TotalCount:              52,
		SavedNoDueDateCount:     6,
		TodoCount:               36,
		TodoUnfilteredCount:     36,
	}

	assert.Equal(t, 51, counts.UncompletedCount)
	assert.Equal(t, 1, counts.UncompletedOverdueCount)
	assert.Equal(t, 52, counts.TotalCount)
	assert.Equal(t, 6, counts.SavedNoDueDateCount)
	assert.Equal(t, 36, counts.TodoCount)
	assert.Equal(t, 0, counts.CompletedCount)
	assert.Equal(t, 0, counts.ArchivedCount)
}

func TestUnitFormatUnixTs(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{
			name:     "zero returns empty string",
			input:    0,
			expected: "",
		},
		{
			name:     "valid timestamp formats correctly",
			input:    1772720838,
			expected: "2026-03-05 14:27",
		},
		{
			name:     "another valid timestamp",
			input:    1772521200,
			expected: "2026-03-03 07:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatUnixTs(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUnitSavedUpdateParamValidation(t *testing.T) {
	t.Run("item_id and ts are both required", func(t *testing.T) {
		assert.NotEmpty(t, "C092WJP9Z38", "item_id must be non-empty")
		assert.NotEmpty(t, "1772034406.593509", "ts must be non-empty")
	})

	t.Run("mark values", func(t *testing.T) {
		validMarks := []string{"completed", ""}
		for _, m := range validMarks {
			assert.Contains(t, []string{"completed", ""}, m)
		}
	})
}

func TestUnitSavedListResponseParsing(t *testing.T) {
	resp := edge.SavedListResponse{
		SavedItems: []edge.SavedItem{
			{
				ItemID:      "D0AGSQXLJHG",
				ItemType:    "message",
				DateCreated: 1771942696,
				DateDue:     1772521200,
				Ts:          "1771941381.234049",
				State:       "in_progress",
			},
			{
				ItemID:      "C0AJMCRNH0U",
				ItemType:    "message",
				DateCreated: 1772720838,
				DateDue:     0,
				Ts:          "1772680334.954409",
				State:       "in_progress",
			},
		},
		Counts: edge.SavedCounts{
			UncompletedCount: 51,
			TotalCount:       52,
		},
	}

	assert.Len(t, resp.SavedItems, 2)
	assert.Equal(t, 51, resp.Counts.UncompletedCount)

	first := resp.SavedItems[0]
	assert.Equal(t, "D0AGSQXLJHG", first.ItemID)
	assert.Equal(t, int64(1772521200), first.DateDue)

	second := resp.SavedItems[1]
	assert.Equal(t, int64(0), second.DateDue)
}

func TestUnitSavedListTodoPayload(t *testing.T) {
	raw := []byte(`{
		"ok": true,
		"saved_items": [{
			"item_id": "G790S2EV6",
			"item_type": "message",
			"date_created": 1793398393,
			"date_due": 1793398393,
			"date_completed": 0,
			"date_updated": 1793398393,
			"is_archived": false,
			"date_snoozed_until": 0,
			"ts": "1793396649.218439",
			"state": "in_progress",
			"todo_state": "to_do"
		}, {
			"item_id": "",
			"item_type": "reminder",
			"description": "send the notes",
			"date_due": 1793400000,
			"state": "in_progress",
			"todo_state": "to_do"
		}],
		"counts": {
			"uncompleted_count": 6,
			"uncompleted_overdue_count": 0,
			"archived_count": 0,
			"completed_count": 551,
			"total_count": 593,
			"saved_no_due_date_count": 6,
			"todo_unfiltered_count": 36,
			"todo_count": 36
		}
	}`)

	var resp edge.SavedListResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Len(t, resp.SavedItems, 2)
	assert.Equal(t, "to_do", resp.SavedItems[0].TodoState)
	assert.Equal(t, "reminder", resp.SavedItems[1].ItemType)
	assert.Equal(t, "send the notes", resp.SavedItems[1].Description)
	assert.Equal(t, 6, resp.Counts.SavedNoDueDateCount)
	assert.Equal(t, 36, resp.Counts.TodoCount)
	assert.Equal(t, 36, resp.Counts.TodoUnfilteredCount)
}

func TestUnitSavedListFilters(t *testing.T) {
	view, filters, err := savedListFilters("all")
	require.NoError(t, err)
	assert.Equal(t, "all", view)
	assert.Equal(t, []string{"saved", "todo"}, filters)

	view, filters, err = savedListFilters("to_do")
	require.NoError(t, err)
	assert.Equal(t, "to_do", view)
	assert.Equal(t, []string{"todo"}, filters)

	view, filters, err = savedListFilters("saved")
	require.NoError(t, err)
	assert.Equal(t, []string{"saved"}, filters)

	_, _, err = savedListFilters("in_progress")
	assert.Error(t, err)
}

func TestUnitSavedItemView(t *testing.T) {
	todo := edge.SavedItem{State: "in_progress", TodoState: "to_do", DateDue: 1793398393}
	saved := edge.SavedItem{State: "in_progress", TodoState: "saved"}
	legacyTodo := edge.SavedItem{State: "in_progress", DateDue: 100}
	legacySaved := edge.SavedItem{State: "in_progress"}
	done := edge.SavedItem{State: "completed", TodoState: "to_do"}
	archived := edge.SavedItem{IsArchived: true, TodoState: "saved"}

	assert.Equal(t, "to_do", savedItemView(todo))
	assert.Equal(t, "saved", savedItemView(saved))
	assert.Equal(t, "to_do", savedItemView(legacyTodo))
	assert.Equal(t, "saved", savedItemView(legacySaved))
	assert.Equal(t, "completed", savedItemView(done))
	assert.Equal(t, "archived", savedItemView(archived))

	assert.True(t, savedItemMatches(todo, "all"))
	assert.True(t, savedItemMatches(saved, "all"))
	assert.False(t, savedItemMatches(done, "all"))
	assert.True(t, savedItemMatches(todo, "to_do"))
	assert.False(t, savedItemMatches(saved, "to_do"))
}

func TestUnitMergeSavedEntriesKeepsBothTabs(t *testing.T) {
	todos := make([]savedListEntry, 40)
	saved := make([]savedListEntry, 40)
	for i := range todos {
		todos[i].row.TodoState = "to_do"
		todos[i].row.ItemID = fmt.Sprintf("todo-%d", i)
	}
	for i := range saved {
		saved[i].row.TodoState = "saved"
		saved[i].row.ItemID = fmt.Sprintf("saved-%d", i)
	}

	merged := mergeSavedEntries("all", map[string][]savedListEntry{
		"todo":  todos,
		"saved": saved,
	}, 50)
	require.Len(t, merged, 50)

	var todoCount, savedCount int
	for _, entry := range merged {
		switch entry.row.TodoState {
		case "to_do":
			todoCount++
		case "saved":
			savedCount++
		}
	}
	assert.Equal(t, 25, todoCount)
	assert.Equal(t, 25, savedCount)

	short := mergeSavedEntries("all", map[string][]savedListEntry{
		"todo":  todos[:3],
		"saved": saved,
	}, 50)
	require.Len(t, short, 43)
	assert.Equal(t, "to_do", short[0].row.TodoState)
	assert.Equal(t, "to_do", short[2].row.TodoState)
	assert.Equal(t, "saved", short[3].row.TodoState)
}

func TestUnitSavedRowSkipsHistoryFieldsForReminders(t *testing.T) {
	row := savedRow(edge.SavedItem{
		ItemType:    "reminder",
		Description: "send the notes",
		State:       "in_progress",
		TodoState:   "to_do",
		DateDue:     1793400000,
	}, map[string]provider.Channel{
		"C123": {ID: "C123", Name: "general"},
	})

	assert.Equal(t, "reminder", row.ItemType)
	assert.Equal(t, "send the notes", row.Text)
	assert.Equal(t, "to_do", row.TodoState)
	assert.Empty(t, row.ChannelID)
	assert.Empty(t, row.ChannelName)

	message := savedRow(edge.SavedItem{
		ItemID:    "C123",
		ItemType:  "message",
		Ts:        "1.2",
		State:     "in_progress",
		TodoState: "saved",
	}, map[string]provider.Channel{
		"C123": {ID: "C123", Name: "general"},
	})
	assert.Equal(t, "C123", message.ChannelID)
	assert.Equal(t, "general", message.ChannelName)
	assert.Equal(t, "saved", message.TodoState)
	assert.Empty(t, message.Text)
}
