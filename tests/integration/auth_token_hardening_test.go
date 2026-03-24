package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestAPITokenCurrentAndSelfRevoke(t *testing.T) {
	ts, _ := setupTestServer(t)
	jwtToken := getAuthToken(t, ts)

	createReqBody := bytes.NewBufferString(`{"name":"IDE Test Session"}`)
	createReq, err := http.NewRequest("POST", ts.URL+"/api/v1/tokens", createReqBody)
	if err != nil {
		t.Fatal(err)
	}
	createReq.Header.Set("Authorization", "Bearer "+jwtToken)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("token creation failed: %d %s", createResp.StatusCode, body)
	}

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	rawToken, _ := created["token"].(string)
	if rawToken == "" {
		t.Fatal("expected raw API token on creation")
	}

	currentReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens/current", nil)
	currentReq.Header.Set("Authorization", "Bearer "+rawToken)
	currentResp, err := http.DefaultClient.Do(currentReq)
	if err != nil {
		t.Fatal(err)
	}
	defer currentResp.Body.Close()
	if currentResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(currentResp.Body)
		t.Fatalf("current token lookup failed: %d %s", currentResp.StatusCode, body)
	}

	var current map[string]any
	if err := json.NewDecoder(currentResp.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if current["token_kind"] != "admin_api" {
		t.Fatalf("expected token_kind admin_api, got %#v", current["token_kind"])
	}
	if current["user_id"] == "" {
		t.Fatal("expected user_id on current token")
	}

	revokeReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/tokens/current/revoke", nil)
	revokeReq.Header.Set("Authorization", "Bearer "+rawToken)
	revokeResp, err := http.DefaultClient.Do(revokeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(revokeResp.Body)
		t.Fatalf("self revoke failed: %d %s", revokeResp.StatusCode, body)
	}

	afterReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens/current", nil)
	afterReq.Header.Set("Authorization", "Bearer "+rawToken)
	afterResp, err := http.DefaultClient.Do(afterReq)
	if err != nil {
		t.Fatal(err)
	}
	defer afterResp.Body.Close()
	if afterResp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(afterResp.Body)
		t.Fatalf("revoked token should be unauthorized, got %d %s", afterResp.StatusCode, body)
	}
}

func TestAPITokenListCanFilterByKind(t *testing.T) {
	ts, _ := setupTestServer(t)
	jwtToken := getAuthToken(t, ts)

	createToken := func(name string) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/tokens", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Authorization", "Bearer "+jwtToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("token creation failed: %d", resp.StatusCode)
		}
	}
	createToken("Admin API Token")

	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens?kind=admin_api", nil)
	listReq.Header.Set("Authorization", "Bearer "+jwtToken)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(listResp.Body)
		t.Fatalf("token list failed: %d %s", listResp.StatusCode, body)
	}

	var tokens []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens) == 0 {
		t.Fatal("expected at least one admin_api token")
	}
	for _, token := range tokens {
		if token["token_kind"] != "admin_api" {
			t.Fatalf("expected only admin_api tokens, got %#v", token["token_kind"])
		}
	}
}

func TestAPITokenRevokeUserAndActiveFilter(t *testing.T) {
	ts, _ := setupTestServer(t)
	jwtToken := getAuthToken(t, ts)

	createToken := func(name string) string {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/tokens", bytes.NewBufferString(`{"name":"`+name+`"}`))
		req.Header.Set("Authorization", "Bearer "+jwtToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("token creation failed: %d %s", resp.StatusCode, body)
		}
		var created map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatal(err)
		}
		token, _ := created["token"].(string)
		return token
	}

	firstToken := createToken("Session One")
	_ = createToken("Session Two")

	currentReqBefore, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens/current", nil)
	currentReqBefore.Header.Set("Authorization", "Bearer "+firstToken)
	currentRespBefore, err := http.DefaultClient.Do(currentReqBefore)
	if err != nil {
		t.Fatal(err)
	}
	defer currentRespBefore.Body.Close()
	if currentRespBefore.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(currentRespBefore.Body)
		t.Fatalf("current token lookup failed: %d %s", currentRespBefore.StatusCode, body)
	}
	var current map[string]any
	if err := json.NewDecoder(currentRespBefore.Body).Decode(&current); err != nil {
		t.Fatal(err)
	}
	userID, _ := current["user_id"].(string)
	if userID == "" {
		t.Fatal("expected user_id on current token")
	}

	revokeReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/tokens/revoke-user", bytes.NewBufferString(`{"user_id":"`+userID+`"}`))
	revokeReq.Header.Set("Authorization", "Bearer "+jwtToken)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeResp, err := http.DefaultClient.Do(revokeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(revokeResp.Body)
		t.Fatalf("revoke-user failed: %d %s", revokeResp.StatusCode, body)
	}

	// getAuthToken/setup path creates an OSS admin JWT for the local user. API tokens created from that
	// request should no longer authenticate after bulk revoke.
	currentReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens/current", nil)
	currentReq.Header.Set("Authorization", "Bearer "+firstToken)
	currentResp, err := http.DefaultClient.Do(currentReq)
	if err != nil {
		t.Fatal(err)
	}
	defer currentResp.Body.Close()
	if currentResp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(currentResp.Body)
		t.Fatalf("bulk revoked token should be unauthorized, got %d %s", currentResp.StatusCode, body)
	}

	listReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/tokens?active_only=true", nil)
	listReq.Header.Set("Authorization", "Bearer "+jwtToken)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(listResp.Body)
		t.Fatalf("active_only list failed: %d %s", listResp.StatusCode, body)
	}
	var tokens []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&tokens); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected no active tokens after bulk revoke, got %d", len(tokens))
	}
}
