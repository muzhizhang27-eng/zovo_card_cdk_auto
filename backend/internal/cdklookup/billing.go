package cdklookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

var ErrBillingUnavailable = errors.New("账单查询服务暂时不可用，请稍后重试")

const (
	billingRequestLimit  = 1 << 20
	billingResponseLimit = 4 << 20
)

type billingUpstream interface {
	CheckSubscription(context.Context, string) (*cardplatform.SubscriptionCheckResult, error)
}

func serveBilling(w http.ResponseWriter, r *http.Request, upstream Upstream, workers chan struct{}) {
	var input struct {
		TokenInput string `json:"token_input"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, billingRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	if decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(input.TokenInput) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	select {
	case workers <- struct{}{}:
		defer func() { <-workers }()
	case <-ctx.Done():
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	client, ok := upstream.(billingUpstream)
	if !ok {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrBillingUnavailable.Error()})
		return
	}
	result, err := client.CheckSubscription(ctx, input.TokenInput)
	if err != nil {
		var apiErr *cardplatform.APIError
		if errors.As(err, &apiErr) && apiErr.Code != 0 &&
			apiErr.Code != http.StatusUnauthorized && apiErr.Code != http.StatusForbidden &&
			apiErr.HTTPStatus != http.StatusUnauthorized && apiErr.HTTPStatus != http.StatusForbidden &&
			strings.TrimSpace(apiErr.Msg) != "" && len(apiErr.Msg) <= 512 {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": apiErr.Msg})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrBillingUnavailable.Error()})
		return
	}
	if result == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if result.Invoices == nil {
		result.Invoices = []map[string]any{}
	}
	body, err := json.Marshal(result)
	if err != nil || len(body) > billingResponseLimit {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write(body)
}

// CheckSubscription sends a bounded query only to the configured local socket.
// The public process neither receives an owner key nor falls back to HTTP.
func CheckSubscription(ctx context.Context, socket, tokenInput string) (*cardplatform.SubscriptionCheckResult, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, _ := json.Marshal(map[string]string{"token_input": tokenInput})
	if len(body) > billingRequestLimit {
		return nil, ErrBillingUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lookup/billing-check", bytes.NewReader(body))
	if err != nil {
		return nil, ErrBillingUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, ErrBillingUnavailable
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, billingResponseLimit+1))
	if err != nil || len(response) > billingResponseLimit {
		return nil, ErrBillingUnavailable
	}
	if resp.StatusCode == http.StatusBadGateway {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(response, &failure) == nil && strings.TrimSpace(failure.Error) != "" && len(failure.Error) <= 512 {
			return nil, &cardplatform.APIError{HTTPStatus: resp.StatusCode, Msg: failure.Error}
		}
		return nil, ErrBillingUnavailable
	}
	var out cardplatform.SubscriptionCheckResult
	if resp.StatusCode != http.StatusOK || json.Unmarshal(response, &out) != nil || bytes.Equal(bytes.TrimSpace(response), []byte("null")) {
		return nil, ErrBillingUnavailable
	}
	return &out, nil
}
