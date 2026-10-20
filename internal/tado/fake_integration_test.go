package tado

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/cpanato/tado-bridge/internal/credentials"
	"github.com/cpanato/tado-bridge/internal/testutil/tadofake"
)

func TestReusableFakeCollectionAndFailure(t *testing.T) {
	fake := &tadofake.Server{}
	server := httptest.NewServer(fake)
	defer server.Close()
	client := New()
	client.http = server.Client()
	client.authURL = server.URL + "/oauth2"
	client.apiURL = server.URL + "/api/v2"
	client.xAPIURL = server.URL
	token, err := client.Refresh(context.Background(), credentials.Token{RefreshToken: tadofake.InitialRefreshToken})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Read(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Homes) != 2 || len(snapshot.Homes[1].Zones) != 8 || token.RefreshToken != tadofake.RotatedRefreshToken {
		t.Fatal("incorrect fixture contract")
	}
	fake.SetUnavailable(true)
	if _, err := client.Read(context.Background(), token); err == nil {
		t.Fatal("outage was not returned")
	}
	fake.SetUnavailable(false)
	if _, err := client.Read(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if fake.Stats().OAuth != 1 {
		t.Fatal("unexpected refresh count")
	}
}
