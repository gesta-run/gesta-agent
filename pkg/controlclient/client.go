package controlclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const maxEventRequestBodyBytes = 8 * 1024 * 1024

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type httpStatusError struct {
	statusCode int
	status     string
	message    string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("control plane returned %s: %s", e.status, e.message)
}

type rejectedEventError struct {
	eventID string
	cause   error
}

func (e *rejectedEventError) Error() string {
	return e.cause.Error()
}

func (e *rejectedEventError) Unwrap() error {
	return e.cause
}

func (e *rejectedEventError) RejectedEventIDs() []string {
	return []string{e.eventID}
}

func NewClient(controlURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(controlURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Heartbeat(req model.HeartbeatRequest) (model.HeartbeatResponse, error) {
	var resp model.HeartbeatResponse
	if err := c.post("/api/v1/heartbeat", c.token, req, &resp); err != nil {
		return model.HeartbeatResponse{}, err
	}
	return resp, nil
}

func (c *Client) SendEvents(events []model.EventEnvelope) error {
	return c.SendEventsForDaemon(events, "", "")
}

// SendEventsForDaemon binds queued events to the currently authenticated
// daemon. This keeps events created before a local re-enrollment from blocking
// the current daemon's queue with a stale identity.
func (c *Client) SendEventsForDaemon(events []model.EventEnvelope, daemonID, deviceID string) error {
	events = filterUploadEvents(events)
	if len(events) == 0 {
		return nil
	}
	daemonID = strings.TrimSpace(daemonID)
	deviceID = strings.TrimSpace(deviceID)
	for i := range events {
		events[i].UserID = ""
		events[i].UserName = ""
		if daemonID != "" {
			events[i].DaemonID = daemonID
		}
		if deviceID != "" {
			events[i].DeviceID = deviceID
		}
	}
	headers := map[string]string{
		model.EventProtocolHeader: model.EventProtocolVersion,
	}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}
	batches, err := splitEventBatchesByEncodedSize(events, maxEventRequestBodyBytes)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if err := c.sendEventBatch(headers, batch); err != nil {
			return err
		}
	}
	return nil
}

func splitEventBatchesByEncodedSize(events []model.EventEnvelope, maxBytes int) ([][]model.EventEnvelope, error) {
	if len(events) == 0 {
		return nil, nil
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("event request body limit must be positive")
	}

	const eventBatchJSONOverhead = len(`{"events":[]}`)
	batches := make([][]model.EventEnvelope, 0, 1)
	current := make([]model.EventEnvelope, 0, len(events))
	currentBytes := eventBatchJSONOverhead
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("marshal event %s: %w", event.EventID, err)
		}
		separatorBytes := 0
		if len(current) > 0 {
			separatorBytes = 1
		}
		if len(current) > 0 && currentBytes+separatorBytes+len(encoded) > maxBytes {
			batches = append(batches, current)
			current = make([]model.EventEnvelope, 0, len(events)-len(current))
			currentBytes = eventBatchJSONOverhead
			separatorBytes = 0
		}
		current = append(current, event)
		currentBytes += separatorBytes + len(encoded)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches, nil
}

func (c *Client) sendEventBatch(headers map[string]string, events []model.EventEnvelope) error {
	var resp map[string]interface{}
	err := c.postWithHeaders("/api/v1/events", headers, model.EventBatch{Events: events}, &resp)
	statusErr, isHTTPError := err.(*httpStatusError)
	if !isHTTPError || !isPermanentEventBatchRejection(statusErr.statusCode) {
		return err
	}
	if len(events) == 1 {
		return &rejectedEventError{eventID: events[0].EventID, cause: err}
	}

	middle := len(events) / 2
	if err := c.sendEventBatch(headers, events[:middle]); err != nil {
		return err
	}
	return c.sendEventBatch(headers, events[middle:])
}

func isPermanentEventBatchRejection(statusCode int) bool {
	return statusCode == http.StatusBadRequest || statusCode == http.StatusRequestEntityTooLarge
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

func (c *Client) ContextRules() (model.ContextRuleBundle, error) {
	var bundle model.ContextRuleBundle
	if err := c.get("/api/v1/context-rules", c.token, &bundle); err != nil {
		return model.ContextRuleBundle{}, err
	}
	if bundle.Rules == nil {
		bundle.Rules = []model.ContextRule{}
	}
	return bundle, nil
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
		return decodeHTTPStatusError(resp)
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
		return decodeHTTPStatusError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func decodeHTTPStatusError(resp *http.Response) error {
	var apiErr model.APIError
	_ = json.NewDecoder(resp.Body).Decode(&apiErr)
	if apiErr.Error == "" {
		apiErr.Error = resp.Status
	}
	return &httpStatusError{
		statusCode: resp.StatusCode,
		status:     resp.Status,
		message:    apiErr.Error,
	}
}
