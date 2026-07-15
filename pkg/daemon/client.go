package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(controlURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(controlURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Enroll(req model.EnrollmentRequest) (model.EnrollmentResponse, error) {
	return c.EnrollWithAPIKey(req, req.APIKey)
}

func (c *Client) EnrollWithAPIKey(req model.EnrollmentRequest, apiKey string) (model.EnrollmentResponse, error) {
	var resp model.EnrollmentResponse
	if apiKey == "" {
		apiKey = req.APIKey
	}
	req.APIKey = ""
	headers := map[string]string{}
	if apiKey != "" {
		headers["X-API-Key"] = apiKey
	}
	if err := c.postWithHeaders("/api/v1/enroll", headers, req, &resp); err != nil {
		return model.EnrollmentResponse{}, err
	}
	return resp, nil
}

func (c *Client) Heartbeat(req model.HeartbeatRequest) (model.HeartbeatResponse, error) {
	var resp model.HeartbeatResponse
	if err := c.post("/api/v1/heartbeat", c.token, req, &resp); err != nil {
		return model.HeartbeatResponse{}, err
	}
	return resp, nil
}

func (c *Client) SendEvents(events []model.EventEnvelope) error {
	events = filterUploadEvents(events)
	if len(events) == 0 {
		return nil
	}
	var resp map[string]interface{}
	return c.post("/api/v1/events", c.token, model.EventBatch{Events: events}, &resp)
}

func filterUploadEvents(events []model.EventEnvelope) []model.EventEnvelope {
	if len(events) == 0 {
		return events
	}
	out := make([]model.EventEnvelope, 0, len(events))
	for _, event := range events {
		if event.EventType == "policy.decision" && !policyDecisionHasRuleMatch(event.Payload) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func policyDecisionHasRuleMatch(payload map[string]interface{}) bool {
	if strings.TrimSpace(stringFromPayload(payload, "rule_id", "matched_rule_id")) != "" {
		return true
	}
	value, ok := payload["rule_ids"]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	case []interface{}:
		for _, item := range typed {
			if strings.TrimSpace(fmt.Sprint(item)) != "" {
				return true
			}
		}
	}
	return false
}

func stringFromPayload(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok && value != nil {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				return text
			}
		}
	}
	return ""
}

func (c *Client) PolicyRules() ([]model.PolicyRule, error) {
	var resp model.PolicyRulesResponse
	if err := c.get("/api/v1/policies", c.token, &resp); err != nil {
		return nil, err
	}
	if resp.Rules == nil {
		resp.Rules = []model.PolicyRule{}
	}
	return resp.Rules, nil
}

func (c *Client) SensitiveRules() ([]model.SensitiveRule, error) {
	var resp model.SensitiveRulesResponse
	if err := c.get("/api/v1/sensitive-rules", c.token, &resp); err != nil {
		return nil, err
	}
	if resp.Rules == nil {
		resp.Rules = []model.SensitiveRule{}
	}
	return resp.Rules, nil
}

func (c *Client) CreatePolicyApproval(req model.CreatePolicyApprovalRequest) (model.PolicyApproval, error) {
	var resp model.PolicyApproval
	if err := c.post("/api/v1/policyapprovals", c.token, req, &resp); err != nil {
		return model.PolicyApproval{}, err
	}
	return resp, nil
}

func (c *Client) ConsumePolicyApproval(req model.ConsumePolicyApprovalRequest) (model.PolicyApprovalResolveResponse, error) {
	var resp model.PolicyApprovalResolveResponse
	if err := c.post("/api/v1/policyapprovals/consume", c.token, req, &resp); err != nil {
		return model.PolicyApprovalResolveResponse{}, err
	}
	return resp, nil
}

func (c *Client) get(path, token string, out interface{}) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr model.APIError
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error == "" {
			apiErr.Error = resp.Status
		}
		return fmt.Errorf("control plane returned %s: %s", resp.Status, apiErr.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) post(path, token string, body interface{}, out interface{}) error {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return c.postWithHeaders(path, headers, body, out)
}

func (c *Client) postWithHeaders(path string, headers map[string]string, body interface{}, out interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr model.APIError
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error == "" {
			apiErr.Error = resp.Status
		}
		return fmt.Errorf("control plane returned %s: %s", resp.Status, apiErr.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
