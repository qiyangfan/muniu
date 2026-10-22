package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/solvela/muniu/internal/protocol"
)

// NormalizeMultimodalEmbeddingUsage 归一两个受支持快照的原生输入用量；无效字段返回部分已知值和错误。
func (o *OpenAI) NormalizeMultimodalEmbeddingUsage(raw json.RawMessage, upstreamModel string) (protocol.Usage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("usage 须为 JSON 对象")
		}
		return protocol.Usage{}, fmt.Errorf("adapter: 原生多模态 usage 非法: %w", err)
	}
	var usage protocol.Usage
	switch upstreamModel {
	case "qwen3-vl-embedding":
		textTokens, hasText, textErr := nonnegativeToken(fields, "input_tokens")
		visionTokens, hasVision, visionErr := nonnegativeToken(fields, "image_tokens")
		totalTokens, hasTotal, totalErr := nonnegativeToken(fields, "total_tokens")
		if hasTotal && totalErr == nil {
			usage.PromptTokens = totalTokens
		}
		if hasVision && visionErr == nil {
			usage.VisionTokens = visionTokens
		}
		if hasText && textErr == nil && hasVision && visionErr == nil {
			if textTokens > math.MaxInt64-visionTokens {
				return protocol.Usage{}, errors.New("adapter: Qwen token 总量溢出")
			}
			sum := textTokens + visionTokens
			usage.PromptTokens = sum
			if hasTotal && totalErr == nil && sum != totalTokens {
				return usage, errors.New("adapter: Qwen usage 分项与 total_tokens 不一致")
			}
		} else if !hasTotal && totalErr == nil {
			if hasText && textErr == nil {
				usage.PromptTokens = textTokens
			} else if hasVision && visionErr == nil {
				usage.PromptTokens = visionTokens
			}
		}
		if textErr != nil || visionErr != nil || totalErr != nil || !hasText || !hasVision {
			return usage, errors.Join(textErr, visionErr, totalErr, missingTokenError(!hasText, "input_tokens"), missingTokenError(!hasVision, "image_tokens"))
		}
		return usage, nil
	case "tongyi-embedding-vision-flash":
		totalTokens, hasTotal, totalErr := nonnegativeToken(fields, "input_tokens")
		if hasTotal && totalErr == nil {
			usage.PromptTokens = totalTokens
		}
		var details map[string]json.RawMessage
		if rawDetails, ok := fields["input_tokens_details"]; ok {
			if err := json.Unmarshal(rawDetails, &details); err != nil || details == nil {
				if err == nil {
					err = errors.New("须为 JSON 对象")
				}
				return usage, errors.Join(totalErr, fmt.Errorf("adapter: Tongyi input_tokens_details 非法: %w", err))
			}
		}
		visionTokens, hasVision, visionErr := nonnegativeToken(details, "image_tokens")
		if hasVision && visionErr == nil {
			if !hasTotal || totalErr != nil || visionTokens > totalTokens {
				return usage, errors.Join(totalErr, errors.New("adapter: Tongyi image_tokens 超出或缺少总量"))
			}
			usage.VisionTokens = visionTokens
		}
		if totalErr != nil || !hasTotal || visionErr != nil || !hasVision {
			return usage, errors.Join(totalErr, visionErr, missingTokenError(!hasTotal, "input_tokens"), missingTokenError(!hasVision, "input_tokens_details.image_tokens"))
		}
		return usage, nil
	default:
		return usage, fmt.Errorf("adapter: 不支持的多模态向量模型 %q", upstreamModel)
	}
}

func nonnegativeToken(fields map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, false, nil
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, true, fmt.Errorf("adapter: %s 须为非负整数", key)
	}
	return value, true, nil
}

func missingTokenError(missing bool, field string) error {
	if missing {
		return fmt.Errorf("adapter: usage 缺少 %s", field)
	}
	return nil
}
