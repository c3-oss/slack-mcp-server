package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// DraftTimestamp accepts Slack timestamps encoded as either JSON strings or numbers.
type DraftTimestamp string

func (t *DraftTimestamp) UnmarshalJSON(data []byte) error {
	var s string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
	} else {
		var n json.Number
		if err := json.Unmarshal(data, &n); err != nil {
			return err
		}
		s = n.String()
	}
	*t = DraftTimestamp(s)
	return nil
}

func (t DraftTimestamp) String() string { return string(t) }

// DraftDestination describes the channel and optional thread target of a draft.
type DraftDestination struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Broadcast bool   `json:"broadcast,omitempty"`
}

// Draft is a Slack message draft returned by the webclient API.
type Draft struct {
	ID            string             `json:"id"`
	LastUpdatedTs string             `json:"last_updated_ts"`
	Destinations  []DraftDestination `json:"destinations"`
	Blocks        json.RawMessage    `json:"blocks"`
	FileIDs       []string           `json:"file_ids"`
	DateScheduled DraftTimestamp     `json:"date_scheduled"`
	DateCreated   DraftTimestamp     `json:"date_created"`
	IsSent        bool               `json:"is_sent"`
	IsDeleted     bool               `json:"is_deleted"`
}

type DraftsListResponse struct {
	baseResponse
	Drafts []Draft `json:"drafts"`
}
type draftMutationResponse struct {
	baseResponse
	Draft Draft `json:"draft"`
}

// DraftInput contains the user-editable fields for creating or updating a draft.
type DraftInput struct {
	ChannelID     string
	Text          string
	ThreadTS      string
	Broadcast     bool
	FileIDs       []string
	DateScheduled string
}

func padDraftTs(ts string) string {
	if i := strings.IndexByte(ts, '.'); i >= 0 {
		return ts[:i+1] + ts[i+1:] + strings.Repeat("0", max(0, 7-len(ts[i+1:])))
	}
	return ts
}

type richTextElement struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
type richTextSection struct {
	Type     string            `json:"type"`
	Elements []richTextElement `json:"elements"`
}
type richTextBlock struct {
	Type     string            `json:"type"`
	Elements []richTextSection `json:"elements"`
}

// draftBody builds the Slack payload. Slack requires blocks, destinations, and
// file_ids to be JSON strings nested inside the outer JSON body.
func draftBody(in DraftInput) (map[string]any, error) {
	blocks := []richTextBlock{{Type: "rich_text", Elements: []richTextSection{{Type: "rich_text_section", Elements: []richTextElement{{Type: "text", Text: in.Text}}}}}}
	destination := DraftDestination{ChannelID: in.ChannelID}
	if in.ThreadTS != "" {
		destination.ThreadTS, destination.Broadcast = in.ThreadTS, in.Broadcast
	}
	encode := func(value any) (string, error) { data, err := json.Marshal(value); return string(data), err }
	blocksJSON, err := encode(blocks)
	if err != nil {
		return nil, err
	}
	destinationsJSON, err := encode([]DraftDestination{destination})
	if err != nil {
		return nil, err
	}
	fileIDs := in.FileIDs
	if fileIDs == nil {
		fileIDs = []string{}
	}
	fileIDsJSON, err := encode(fileIDs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"blocks": blocksJSON, "destinations": destinationsJSON, "file_ids": fileIDsJSON, "client_msg_id": uuid.New().String()}, nil
}

// PostJSONWebclient posts a JSON request to Slack's session webclient API.
func (cl *Client) PostJSONWebclient(ctx context.Context, method string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	tape := cl.recorder(bytes.NewReader(data))
	defer cl.record([]byte("\n\n"))
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.webclientAPI+method, tape)
	if err != nil {
		return nil, err
	}
	r.Header.Set(hdrContentType, "application/json")
	r.Header.Set("Authorization", "Bearer "+cl.token)
	return do(ctx, cl.cl, r)
}

func (cl *Client) DraftsList(ctx context.Context, isActive *bool, limit int) (DraftsListResponse, error) {
	body := map[string]any{}
	if isActive != nil {
		body["is_active"] = fmt.Sprintf("%t", *isActive)
	}
	if limit != 0 {
		body["limit"] = fmt.Sprintf("%d", limit)
	}
	resp, err := cl.PostJSONWebclient(ctx, "drafts.list", body)
	if err != nil {
		return DraftsListResponse{}, err
	}
	var out DraftsListResponse
	if err := cl.ParseResponse(&out, resp); err != nil {
		return out, err
	}
	return out, out.validate("drafts.list")
}

// DraftsCreate creates a draft and returns Slack's resulting draft record.
func (cl *Client) DraftsCreate(ctx context.Context, in DraftInput) (Draft, error) {
	body, err := draftBody(in)
	if err != nil {
		return Draft{}, err
	}
	body["is_from_composer"] = "true"
	if in.DateScheduled != "" {
		body["date_scheduled"] = in.DateScheduled
	}
	resp, err := cl.PostJSONWebclient(ctx, "drafts.create", body)
	if err != nil {
		return Draft{}, err
	}
	var out draftMutationResponse
	if err := cl.ParseResponse(&out, resp); err != nil {
		return Draft{}, err
	}
	if err := out.validate("drafts.create"); err != nil {
		return Draft{}, err
	}
	return out.Draft, nil
}

// DraftsUpdate updates a draft and returns Slack's resulting draft record.
func (cl *Client) DraftsUpdate(ctx context.Context, draftID, ts string, in DraftInput) (Draft, error) {
	body, err := draftBody(in)
	if err != nil {
		return Draft{}, err
	}
	body["draft_id"], body["client_last_updated_ts"] = draftID, padDraftTs(ts)
	resp, err := cl.PostJSONWebclient(ctx, "drafts.update", body)
	if err != nil {
		return Draft{}, err
	}
	var out draftMutationResponse
	if err := cl.ParseResponse(&out, resp); err != nil {
		return Draft{}, err
	}
	if err := out.validate("drafts.update"); err != nil {
		return Draft{}, err
	}
	return out.Draft, nil
}

// DraftsDelete deletes a draft using Slack's form-encoded endpoint.
func (cl *Client) DraftsDelete(ctx context.Context, draftID, ts string) error {
	resp, err := cl.PostForm(ctx, "drafts.delete", url.Values{"draft_id": {draftID}, "client_last_updated_ts": {padDraftTs(ts)}})
	if err != nil {
		return err
	}
	var out baseResponse
	if err := cl.ParseResponse(&out, resp); err != nil {
		return err
	}
	return out.validate("drafts.delete")
}
