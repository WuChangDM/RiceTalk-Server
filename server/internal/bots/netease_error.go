package bots

import (
	"encoding/json"
	"fmt"

	apperrors "ridgericetalk/core/errors"
)

// neteaseResultError converts actionable upstream challenges into stable
// RidgeRiceTalk error codes. Only a strict metadata allowlist is returned:
// verifyToken and request-signing fields must never reach clients or logs.
func neteaseResultError(result map[string]interface{}) error {
	if !isNeteaseVerificationRequired(result["code"]) {
		return nil
	}

	details := map[string]interface{}{
		"upstreamCode": -462,
	}
	copyNeteaseVerificationField(details, result, "verifyType", "verifyType")
	copyNeteaseVerificationField(details, result, "verifyId", "verifyId")
	if _, ok := result["verifyUrl"]; ok {
		copyNeteaseVerificationField(details, result, "verifyUrl", "verifyUrl")
	} else {
		copyNeteaseVerificationField(details, result, "verifyURL", "verifyUrl")
	}

	detailJSON, _ := json.Marshal(details)
	return apperrors.New(
		apperrors.BOT_NETEASE_VERIFICATION_REQUIRED,
		"netease verification required",
	).WithDetails(string(detailJSON))
}

func neteaseUpstreamError(status int, body []byte) error {
	var result map[string]interface{}
	if json.Unmarshal(body, &result) == nil {
		if err := neteaseResultError(result); err != nil {
			return err
		}
	}
	return apperrors.New(
		apperrors.BOT_NETEASE_API_ERROR,
		fmt.Sprintf("Netease API returned status %d", status),
	)
}

func isNeteaseVerificationRequired(code interface{}) bool {
	switch value := code.(type) {
	case float64:
		return int(value) == -462
	case int:
		return value == -462
	case json.Number:
		return value.String() == "-462"
	case string:
		return value == "-462"
	default:
		return false
	}
}

func copyNeteaseVerificationField(dst, src map[string]interface{}, sourceKey, targetKey string) {
	value, ok := src[sourceKey]
	if !ok || value == nil {
		return
	}
	switch value.(type) {
	case string, float64, int, json.Number:
		dst[targetKey] = value
	}
}
