package sdkmodels

import (
	"slices"
	"testing"
)

func TestResponsesCapabilities(t *testing.T) {
	known := ResponsesCapabilities("gpt-6.1-sol")
	if known.Reasoning != "effort-mapping" || !slices.Contains(known.Efforts, "low") || known.ThinkingOff != "lowest-effort" {
		t.Fatalf("已知模型的映射能力错误：%+v", known)
	}
	if known.OutputLimit != "not-enforced" || known.ContextWindow != "unverified" || len(known.Warnings) < 5 {
		t.Fatalf("未准确声明限制：%+v", known)
	}
	known.Efforts[0] = "changed"
	if slices.Contains(ResponsesCapabilities("gpt-6.1-sol").Efforts, "changed") {
		t.Fatal("调用方修改影响了共享能力目录")
	}
	unknown := ResponsesCapabilities("unknown-model")
	if unknown.Reasoning != "unavailable" || len(unknown.Efforts) != 0 || unknown.ContextWindow != "unverified" {
		t.Fatalf("未知模型虚报能力：%+v", unknown)
	}
}
