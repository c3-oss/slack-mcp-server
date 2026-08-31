package edge

import (
	"context"
	"encoding/json"
	"runtime/trace"

	"github.com/slack-go/slack"
)

type chatShareMessageForm struct {
	BaseRequest
	Channel      string `json:"channel"`
	Timestamp    string `json:"timestamp"`
	ShareChannel string `json:"share_channel"`
	Blocks       string `json:"blocks,omitempty"`
}

// ChatShareMessageResponse is returned by Slack's internal chat.shareMessage
// endpoint after it creates a native message-forward attachment.
type ChatShareMessageResponse struct {
	baseResponse
	Channel   string        `json:"channel"`
	Timestamp string        `json:"ts"`
	Message   slack.Message `json:"message"`
}

// ShareMessage forwards a Slack message as a native share. Slack builds the
// embedded quote attachment from channel and timestamp; blocks contain only
// the optional forwarding comment.
func (cl *Client) ShareMessage(ctx context.Context, sourceChannel, sourceTimestamp, destinationChannel string, blocks []slack.Block) (ChatShareMessageResponse, error) {
	ctx, task := trace.NewTask(ctx, "ShareMessage")
	defer task.End()

	var blocksJSON string
	if len(blocks) > 0 {
		encoded, err := json.Marshal(blocks)
		if err != nil {
			return ChatShareMessageResponse{}, err
		}
		blocksJSON = string(encoded)
	}

	form := chatShareMessageForm{
		BaseRequest:  BaseRequest{Token: cl.token},
		Channel:      sourceChannel,
		Timestamp:    sourceTimestamp,
		ShareChannel: destinationChannel,
		Blocks:       blocksJSON,
	}

	resp, err := cl.PostForm(ctx, "chat.shareMessage", values(form, true))
	if err != nil {
		return ChatShareMessageResponse{}, err
	}

	result := ChatShareMessageResponse{}
	if err := cl.ParseResponse(&result, resp); err != nil {
		return ChatShareMessageResponse{}, err
	}
	if err := result.validate("chat.shareMessage"); err != nil {
		return ChatShareMessageResponse{}, err
	}
	if result.Timestamp == "" {
		result.Timestamp = result.Message.Timestamp
	}

	return result, nil
}
