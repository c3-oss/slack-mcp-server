package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnitShareMessageContextRejectsOAuthTokens(t *testing.T) {
	client := &MCPSlackClient{isOAuth: true}
	_, _, err := client.ShareMessageContext(context.Background(), "CSOURCE", "1723456789.123456", "CDEST", nil)
	assert.ErrorContains(t, err, "requires browser session authentication")
}
