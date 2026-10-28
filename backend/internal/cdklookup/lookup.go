// Package cdklookup exposes only exact-code, read-only CDK status lookups.
// The Unix socket keeps the owner's API key in the administration service's
// security boundary; the public web process never receives that key.
package cdklookup

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
)

var ErrUnavailable = errors.New("卡密查询服务暂时不可用，请稍后重试")

type Result struct {
	CDKCode      string  `json:"cdk_code"`
	Status       string  `json:"status"`
	Used         bool    `json:"used"`
	CanResubmit  bool    `json:"can_resubmit"`
	AccountEmail string  `json:"account_email,omitempty"`
	Plan         string  `json:"plan,omitempty"`
	UsedAt       *string `json:"used_at,omitempty"`
	Notes        string  `json:"notes,omitempty"`
	Message      string  `json:"message"`
}

func ValidCode(code string) bool {
	if len(code) < 20 || len(code) > 128 || !strings.Contains(code, "-") {
		return false
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

type Upstream interface {
	ListCDKsQuery(context.Context, cardplatform.CDKListQuery) (*cardplatform.CDKListResult, error)
	ListCDKOrdersQuery(context.Context, cardplatform.CDKOrderListQuery) (json.RawMessage, error)
}

func Lookup(ctx context.Context, upstream Upstream, code string) (Result, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	out := Result{CDKCode: code, Status: "unknown", Message: "未找到该卡密。请确认输入完整卡密。"}
	if !ValidCode(code) {
		return out, nil
	}
	list, err := upstream.ListCDKsQuery(ctx, cardplatform.CDKListQuery{Page: 1, PageSize: 20, Query: code})
	if err != nil {
		return out, ErrUnavailable
	}
	for _, item := range list.List {
		// Upstream q is a search filter, not proof of ownership. Never accept
		// a prefix match, a masked code, or another result from the same page.
		full := strings.ToUpper(strings.TrimSpace(item.FullCodeText()))
		if subtle.ConstantTimeCompare([]byte(full), []byte(code)) != 1 {
			continue
		}
		out.Plan = item.Plan
		switch strings.ToLower(item.Status) {
		case "consumed", "used", "redeemed":
			out.Status, out.Used, out.Message = "used", true, "卡密已使用"
		case "unused", "active", "released":
			out.Status, out.Message = "unused", "卡密未使用"
		case "reserved", "processing", "review":
			out.Status, out.Message = "processing", "卡密兑换处理中，请等待最终结果"
		case "disabled", "frozen":
			out.Status, out.Message = "disabled", "卡密已禁用或冻结"
		case "expired":
			out.Status, out.Message = "expired", "卡密已过期"
		default:
			return out, ErrUnavailable
		}
		if out.Status == "used" || out.Status == "processing" {
			// The code's lifecycle is authoritative even when order enrichment
			// is unavailable. Only return fields intended for a code holder.
			raw, err := upstream.ListCDKOrdersQuery(ctx, cardplatform.CDKOrderListQuery{Page: 1, PageSize: 1, CDKID: item.ID})
			if err == nil {
				var orders struct {
					List []struct {
						CDKID        int64  `json:"cdk_id"`
						AccountEmail string `json:"account_email"`
						CompletedAt  string `json:"completed_at"`
					} `json:"list"`
				}
				if json.Unmarshal(raw, &orders) == nil && len(orders.List) > 0 && orders.List[0].CDKID == item.ID {
					order := orders.List[0]
					out.AccountEmail = order.AccountEmail
					if out.Used && order.CompletedAt != "" {
						out.UsedAt = &order.CompletedAt
					}
				}
			}
		}
		return out, nil
	}
	return out, nil
}

func Handler(upstream Upstream) http.Handler {
	workers := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost || r.URL.Path != "/lookup" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Code string `json:"code"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
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
		out, err := Lookup(ctx, upstream, req.Code)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrUnavailable.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

// Query uses only a local Unix socket, with no HTTP fallback or redirects.
func Query(ctx context.Context, socket, code string) (Result, error) {
	var out Result
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lookup/lookup", bytes.NewReader(body))
	if err != nil {
		return out, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&out) != nil {
		return out, ErrUnavailable
	}
	if out.CDKCode != strings.ToUpper(strings.TrimSpace(code)) || out.Status == "" {
		return Result{}, ErrUnavailable
	}
	return out, nil
}
