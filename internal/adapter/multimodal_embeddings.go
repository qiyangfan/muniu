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
	switch upstreamModel {
	case "qwen3-vl-embedding":
		return normalizeQwenVLUsage(fields)
	case "tongyi-embedding-vision-flash":
		return normalizeTongyiVisionUsage(fields)
	default:
		return protocol.Usage{}, fmt.Errorf("adapter: 不支持的多模态向量模型 %q", upstreamModel)
	}
}

// tokenField 单字段解析结果：值、是否存在、解析错误三态并列，避免重复解析。
type tokenField struct {
	value int64
	has   bool
	ok    bool // 存在且无解析错误
}

func readTokenField(fields map[string]json.RawMessage, key string) tokenField {
	v, has, err := nonnegativeToken(fields, key)
	return tokenField{value: v, has: has, ok: has && err == nil}
}

// normalizeQwenVLUsage qwen3-vl-embedding：prompt=input+image，与 total_tokens 必须一致；
// 缺分项时退回 total 或单项；分项不全会累积错误返回，但保留已解析出的部分用量。
func normalizeQwenVLUsage(fields map[string]json.RawMessage) (protocol.Usage, error) {
	text := readTokenField(fields, "input_tokens")
	vision := readTokenField(fields, "image_tokens")
	total := readTokenField(fields, "total_tokens")
	usage, err := qwenPromptTokens(text, vision, total)
	if vision.ok {
		usage.VisionTokens = vision.value
	}
	if err != nil {
		return usage, err
	}
	if !text.ok || !vision.ok || !total.ok {
		return usage, errors.Join(
			readTokenError(fields, "input_tokens"), readTokenError(fields, "image_tokens"),
			readTokenError(fields, "total_tokens"),
			missingTokenError(!text.has, "input_tokens"),
			missingTokenError(!vision.has, "image_tokens"),
		)
	}
	return usage, nil
}

// qwenPromptTokens 按三字段可用性裁决 prompt 总量：分项齐备则求和兼对账，
// 否则以 total 优先、单项次之，全缺为 0。
func qwenPromptTokens(text, vision, total tokenField) (protocol.Usage, error) {
	var usage protocol.Usage
	if total.ok {
		usage.PromptTokens = total.value
	}
	if text.ok && vision.ok {
		prompt, err := qwenTextVisionSum(text, vision, total)
		usage.PromptTokens = prompt // 不一致时错误照常返回，总量仍按分项求和口径透出
		return usage, err
	}
	if !total.has && !total.ok {
		switch {
		case text.ok:
			usage.PromptTokens = text.value
		case vision.ok:
			usage.PromptTokens = vision.value
		}
	}
	return usage, nil
}

// qwenTextVisionSum 分项齐备时求和并与 total 对账，返回 prompt 总量；不一致时仍返回求和值。
func qwenTextVisionSum(text, vision, total tokenField) (int64, error) {
	if text.value > math.MaxInt64-vision.value {
		return 0, errors.New("adapter: Qwen token 总量溢出")
	}
	sum := text.value + vision.value
	if total.ok && sum != total.value {
		return sum, errors.New("adapter: Qwen usage 分项与 total_tokens 不一致")
	}
	return sum, nil
}

// normalizeTongyiVisionUsage tongyi-embedding-vision-flash：input 为总量，image_tokens 在 details 内；
// details 整体非法直接失败，分项缺失/超额累积错误返回。
func normalizeTongyiVisionUsage(fields map[string]json.RawMessage) (protocol.Usage, error) {
	var usage protocol.Usage
	total := readTokenField(fields, "input_tokens")
	if total.ok {
		usage.PromptTokens = total.value
	}
	details, err := tongyiDetails(fields)
	if err != nil {
		return usage, err
	}
	vision := readTokenField(details, "image_tokens")
	if vision.ok {
		if !total.ok || vision.value > total.value {
			return usage, errors.Join(readTokenError(fields, "input_tokens"),
				errors.New("adapter: Tongyi image_tokens 超出或缺少总量"))
		}
		usage.VisionTokens = vision.value
	}
	if !total.ok || !vision.ok {
		return usage, errors.Join(
			readTokenError(fields, "input_tokens"),
			readTokenError(details, "image_tokens"),
			missingTokenError(!total.has, "input_tokens"),
			missingTokenError(!vision.has, "input_tokens_details.image_tokens"),
		)
	}
	return usage, nil
}

// tongyiDetails 解析 input_tokens_details；缺省返回 nil（后续字段读取视为缺失）。
// 存在但非 JSON 对象属整体非法，立即失败。
func tongyiDetails(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	rawDetails, ok := fields["input_tokens_details"]
	if !ok {
		return nil, nil
	}
	var details map[string]json.RawMessage
	if err := json.Unmarshal(rawDetails, &details); err != nil || details == nil {
		if err == nil {
			err = errors.New("须为 JSON 对象")
		}
		return nil, errors.Join(readTokenError(fields, "input_tokens"),
			fmt.Errorf("adapter: Tongyi input_tokens_details 非法: %w", err))
	}
	return details, nil
}

// readTokenError 单字段解析错误（缺失或格式非法），供 errors.Join 聚合。
func readTokenError(fields map[string]json.RawMessage, key string) error {
	_, _, err := nonnegativeToken(fields, key)
	return err
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
