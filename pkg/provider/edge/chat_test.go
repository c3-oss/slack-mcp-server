package edge

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitShareMessagePostsNativeForwardForm(t *testing.T) {
	type capturedRequest struct {
		method      string
		path        string
		contentType string
		form        url.Values
		cookie      *http.Cookie
	}
	captured := capturedRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.contentType = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		captured.form = r.PostForm
		captured.cookie, _ = r.Cookie("d")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"CDEST","ts":"1723456790.654321"}`))
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	jar.SetCookies(serverURL, []*http.Cookie{{Name: "d", Value: "test-session-cookie"}})

	client := &Client{
		cl:           &http.Client{Jar: jar},
		webclientAPI: server.URL + "/api/",
		token:        "test-session-token",
		tape:         nopTape{},
	}
	commentBlocks := []slack.Block{
		&slack.RichTextBlock{
			Type: slack.MBTRichText,
			Elements: []slack.RichTextElement{
				&slack.RichTextSection{
					Type: slack.RTESection,
					Elements: []slack.RichTextSectionElement{
						&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "Please review"},
					},
				},
			},
		},
	}

	response, err := client.ShareMessage(context.Background(), "CSOURCE", "1723456789.123456", "CDEST", commentBlocks)
	require.NoError(t, err)
	assert.Equal(t, "CDEST", response.Channel)
	assert.Equal(t, "1723456790.654321", response.Timestamp)

	assert.Equal(t, http.MethodPost, captured.method)
	assert.Equal(t, "/api/chat.shareMessage", captured.path)
	assert.Equal(t, "application/x-www-form-urlencoded", captured.contentType)
	assert.Equal(t, "test-session-token", captured.form.Get("token"))
	assert.Equal(t, "CSOURCE", captured.form.Get("channel"))
	assert.Equal(t, "1723456789.123456", captured.form.Get("timestamp"))
	assert.Equal(t, "CDEST", captured.form.Get("share_channel"))
	assert.JSONEq(t, `[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Please review"}]}]}]`, captured.form.Get("blocks"))
	require.NotNil(t, captured.cookie)
	assert.Equal(t, "test-session-cookie", captured.cookie.Value)
}

func TestUnitShareMessageOmitsEmptyCommentAndReturnsSlackError(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"message_not_found"}`))
	}))
	defer server.Close()

	client := &Client{
		cl:           server.Client(),
		webclientAPI: server.URL + "/api/",
		token:        "test-session-token",
		tape:         nopTape{},
	}
	_, err := client.ShareMessage(context.Background(), "CSOURCE", "1723456789.123456", "CDEST", nil)
	assert.ErrorContains(t, err, "message_not_found")
	assert.NotContains(t, form, "blocks")
}
