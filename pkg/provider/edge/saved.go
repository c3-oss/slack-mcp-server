package edge

import (
	"context"
	"runtime/trace"
)

// saved.* API — internal Slack APIs for the Saved and To-dos lists.
// These replaced the deprecated stars.* API (March 2023).
// Only accessible with browser session tokens (xoxc/xoxd).

type savedListForm struct {
	BaseRequest
	Limit             int    `json:"limit"`
	Filter            string `json:"filter,omitempty"`
	Cursor            string `json:"cursor,omitempty"`
	IncludeTombstones bool   `json:"include_tombstones"`
	WebClientFields
}

type SavedListResponse struct {
	baseResponse
	SavedItems []SavedItem `json:"saved_items"`
	Counts     SavedCounts `json:"counts"`
}

type SavedItem struct {
	ItemID           string `json:"item_id"`
	ItemType         string `json:"item_type"`
	ItemDetail       string `json:"item_detail"`
	Description      string `json:"description"`
	DateCreated      int64  `json:"date_created"`
	DateDue          int64  `json:"date_due"`
	DateCompleted    int64  `json:"date_completed"`
	DateUpdated      int64  `json:"date_updated"`
	IsArchived       bool   `json:"is_archived"`
	DateSnoozedUntil int64  `json:"date_snoozed_until"`
	Ts               string `json:"ts"`
	State            string `json:"state"`
	// TodoState is the tab: "saved" (no due date), "to_do", or "completed".
	TodoState string `json:"todo_state"`
}

type SavedCounts struct {
	UncompletedCount        int `json:"uncompleted_count"`
	UncompletedOverdueCount int `json:"uncompleted_overdue_count"`
	ArchivedCount           int `json:"archived_count"`
	CompletedCount          int `json:"completed_count"`
	TotalCount              int `json:"total_count"`
	SavedNoDueDateCount     int `json:"saved_no_due_date_count"`
	TodoCount               int `json:"todo_count"`
	TodoUnfilteredCount     int `json:"todo_unfiltered_count"`
}

func (cl *Client) SavedList(ctx context.Context, filter string, limit int, cursor string) (SavedListResponse, error) {
	ctx, task := trace.NewTask(ctx, "SavedList")
	defer task.End()

	form := savedListForm{
		BaseRequest:       BaseRequest{Token: cl.token},
		Limit:             limit,
		Filter:            filter,
		Cursor:            cursor,
		IncludeTombstones: true,
		WebClientFields:   webclientReason("saved-api/savedList"),
	}

	resp, err := cl.PostForm(ctx, "saved.list", values(form, true))
	if err != nil {
		return SavedListResponse{}, err
	}
	r := SavedListResponse{}
	if err := cl.ParseResponse(&r, resp); err != nil {
		return SavedListResponse{}, err
	}
	if err := r.validate("saved.list"); err != nil {
		return SavedListResponse{}, err
	}
	return r, nil
}

type savedUpdateForm struct {
	BaseRequest
	ItemType string `json:"item_type"`
	ItemID   string `json:"item_id"`
	Ts       string `json:"ts"`
	Mark     string `json:"mark,omitempty"`
	DateDue  int64  `json:"date_due,omitempty"`
	WebClientFields
}

func (cl *Client) SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error {
	ctx, task := trace.NewTask(ctx, "SavedUpdate")
	defer task.End()

	form := savedUpdateForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		ItemType:        itemType,
		ItemID:          itemID,
		Ts:              ts,
		Mark:            mark,
		DateDue:         dateDue,
		WebClientFields: webclientReason("saved-api/updateSavedMessage"),
	}

	resp, err := cl.PostForm(ctx, "saved.update", values(form, true))
	if err != nil {
		return err
	}
	r := baseResponse{}
	if err := cl.ParseResponse(&r, resp); err != nil {
		return err
	}
	if err := r.validate("saved.update"); err != nil {
		return err
	}
	return nil
}

type savedClearCompletedForm struct {
	BaseRequest
	WebClientFields
}

func (cl *Client) SavedClearCompleted(ctx context.Context) error {
	ctx, task := trace.NewTask(ctx, "SavedClearCompleted")
	defer task.End()

	form := savedClearCompletedForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		WebClientFields: webclientReason("manually_marked_all_completed"),
	}

	resp, err := cl.PostForm(ctx, "saved.clearCompleted", values(form, true))
	if err != nil {
		return err
	}
	r := baseResponse{}
	if err := cl.ParseResponse(&r, resp); err != nil {
		return err
	}
	if err := r.validate("saved.clearCompleted"); err != nil {
		return err
	}
	return nil
}
