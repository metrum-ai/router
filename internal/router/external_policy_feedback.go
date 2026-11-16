// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	externalPolicyFeedbackSchemaVersion  = "external_policy.feedback.v1"
	externalPolicyFeedbackDefaultTO      = 500
	externalPolicyFeedbackDefaultBackof  = 100
	externalPolicyFeedbackDefaultMaxBody = 4096
)

type externalPolicyFeedbackPayload struct {
	SchemaVersion   string                         `json:"schemaVersion"`
	RequestID       string                         `json:"requestId"`
	Group           string                         `json:"group,omitempty"`
	ConversationKey string                         `json:"conversationKey,omitempty"`
	Status          int                            `json:"status"`
	ErrorClass      string                         `json:"errorClass,omitempty"`
	SelectedTarget  *externalPolicyFeedbackTarget  `json:"selectedTarget,omitempty"`
	Usage           *externalPolicyFeedbackUsage   `json:"usage,omitempty"`
	CostUSD         *externalPolicyFeedbackCostUSD `json:"costUsd,omitempty"`
	LatencyMS       int64                          `json:"latencyMs"`
	TTFBMS          *int64                         `json:"ttfbMs,omitempty"`
	ClassLabel      string                         `json:"classLabel,omitempty"`
	CompletedAt     string                         `json:"completedAt"`
}

type externalPolicyFeedbackTarget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Dialect  string `json:"dialect,omitempty"`
}

type externalPolicyFeedbackUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

type externalPolicyFeedbackCostUSD struct {
	Input       float64 `json:"input,omitempty"`
	Output      float64 `json:"output,omitempty"`
	Total       float64 `json:"total,omitempty"`
	BilledTotal float64 `json:"billedTotal,omitempty"`
}

func (s *externalPolicyStrategy) Close() {
	if s == nil {
		return
	}
	if s.feedbackCancel != nil {
		s.feedbackCancel()
	}
	s.feedbackWG.Wait()
}

func (s *externalPolicyStrategy) EnqueueFeedback(payload externalPolicyFeedbackPayload) {
	if s == nil || !s.cfg.Feedback.Enabled {
		return
	}
	if strings.TrimSpace(payload.RequestID) == "" {
		return
	}
	payload.SchemaVersion = externalPolicyFeedbackSchemaVersion
	if payload.CompletedAt == "" {
		payload.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		s.noteFeedbackFailure(payload.RequestID, "marshal_failed")
		return
	}
	limit := s.cfg.Feedback.MaxRequestBytes
	if limit <= 0 {
		limit = externalPolicyFeedbackDefaultMaxBody
	}
	if int64(len(body)) > limit {
		s.noteFeedbackFailure(payload.RequestID, "payload_too_large")
		return
	}
	ctx := s.feedbackCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.feedbackWG.Add(1)
	go func() {
		defer s.feedbackWG.Done()
		if err := s.deliverFeedback(ctx, body); err != nil {
			s.noteFeedbackFailure(payload.RequestID, feedbackFailureClass(err))
		}
	}()
}

func (s *externalPolicyStrategy) deliverFeedback(ctx context.Context, body []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	feedbackURL := strings.TrimSpace(s.cfg.Feedback.URL)
	if feedbackURL == "" {
		feedbackURL = defaultExternalPolicyFeedbackURL(s.cfg.URL)
	}
	u, err := url.Parse(feedbackURL)
	if err != nil {
		return fmt.Errorf("invalid feedback url")
	}
	allowHosts := s.cfg.Feedback.AllowHosts
	if len(allowHosts) == 0 {
		allowHosts = s.cfg.AllowHosts
	}
	allowHTTP := s.cfg.Feedback.AllowHTTP || s.cfg.AllowHTTP
	if err := validateEgressURL(u, allowHosts, allowHTTP, "external policy feedback"); err != nil {
		return err
	}
	retries := s.cfg.Feedback.MaxRetries
	if retries < 0 {
		retries = 0
	}
	backoff := time.Duration(s.cfg.Feedback.RetryBackoffMS) * time.Millisecond
	if backoff <= 0 {
		backoff = externalPolicyFeedbackDefaultBackof * time.Millisecond
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt > 0 {
			timer := time.NewTimer(backoff * time.Duration(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = s.postFeedbackOnce(ctx, u.String(), body)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (s *externalPolicyStrategy) postFeedbackOnce(ctx context.Context, feedbackURL string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, feedbackURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	headers := s.cfg.Feedback.Headers
	if len(headers) == 0 {
		headers = s.cfg.Headers
	}
	for name, value := range headers {
		if !scriptConfigHeaderAllowed(name) {
			return fmt.Errorf("feedback header %s is not allowed", name)
		}
		req.Header.Set(name, value)
	}
	client := s.feedbackClient
	if client == nil {
		client = s.client
	}
	resp, err := client.Do(req)
	if err != nil {
		return auditSafeHTTPError(err, "external policy feedback request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("external policy feedback returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *externalPolicyStrategy) noteFeedbackFailure(requestID, class string) {
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Feedback.OnDeliveryFailure))
	if mode == "ignore" {
		return
	}
	log.Printf("external policy feedback delivery failed request_id=%s error_class=%s", sanitizeFeedbackRequestID(requestID), class)
}

func sanitizeFeedbackRequestID(requestID string) string {
	id := strings.TrimSpace(requestID)
	if id == "" {
		return "unknown"
	}
	if len(id) > 128 {
		return id[:128]
	}
	return id
}

func feedbackFailureClass(err error) string {
	if err == nil {
		return ""
	}
	if errorsIsContextCanceled(err) {
		return "canceled"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "http "):
		return "upstream_http_error"
	case strings.Contains(msg, "not allowed"), strings.Contains(msg, "invalid"):
		return "config_error"
	default:
		return "delivery_failed"
	}
}

func errorsIsContextCanceled(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded || strings.Contains(strings.ToLower(err.Error()), "context canceled")
}

func externalPolicyFeedbackFromLog(rec logRecord, conversationKey string) externalPolicyFeedbackPayload {
	payload := externalPolicyFeedbackPayload{
		SchemaVersion:   externalPolicyFeedbackSchemaVersion,
		RequestID:       rec.RequestID,
		Group:           rec.ResolvedGroup,
		ConversationKey: conversationKey,
		Status:          rec.Status,
		LatencyMS:       rec.LatencyMS,
		TTFBMS:          rec.TTFBMS,
		CompletedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if rec.Error != nil {
		payload.ErrorClass = strings.TrimSpace(*rec.Error)
	}
	if rec.ClassLabel != nil {
		payload.ClassLabel = strings.TrimSpace(*rec.ClassLabel)
	}
	if rec.TargetProvider != "" || rec.TargetModel != "" {
		payload.SelectedTarget = &externalPolicyFeedbackTarget{
			Provider: rec.TargetProvider,
			Model:    rec.TargetModel,
			Dialect:  rec.TargetDialect,
		}
	}
	if rec.Usage.InputTokens != 0 || rec.Usage.OutputTokens != 0 || rec.Usage.TotalTokens != 0 {
		payload.Usage = &externalPolicyFeedbackUsage{
			InputTokens:  rec.Usage.InputTokens,
			OutputTokens: rec.Usage.OutputTokens,
			TotalTokens:  rec.Usage.TotalTokens,
		}
	}
	if rec.InputCostUSD != 0 || rec.OutputCostUSD != 0 || rec.TotalCostUSD != 0 || rec.UpstreamReportedTotalCostUSD != 0 {
		payload.CostUSD = &externalPolicyFeedbackCostUSD{
			Input:       rec.InputCostUSD,
			Output:      rec.OutputCostUSD,
			Total:       rec.TotalCostUSD,
			BilledTotal: rec.UpstreamReportedTotalCostUSD,
		}
	}
	return payload
}

func newExternalPolicyFeedbackClient(cfg ExternalPolicyConfig) *http.Client {
	timeoutMS := cfg.Feedback.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = externalPolicyFeedbackDefaultTO
	}
	allowHosts := cfg.Feedback.AllowHosts
	if len(allowHosts) == 0 {
		allowHosts = cfg.AllowHosts
	}
	allowHTTP := cfg.Feedback.AllowHTTP || cfg.AllowHTTP
	return newEgressHTTPClient(time.Duration(timeoutMS)*time.Millisecond, allowHosts, allowHTTP, "external policy feedback")
}
