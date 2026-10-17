package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

// 模拟上游的容量拒绝，验证网关不截断正文、不把拒绝伪装为成功，并可继续处理下一请求。
// mock 的24K阈值不是实际Codex容量。
func TestCompatContextRejectionAndRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream-%t", stream), func(t *testing.T) {
			codec, _ := tokenizer.Get(tokenizer.O200kBase)
			var calls atomic.Int32
			observed := make(chan string, 8)
			endpoint, client := compatGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				var texts []string
				var collect func(any)
				collect = func(v any) {
					switch v := v.(type) {
					case []any:
						for _, x := range v {
							collect(x)
						}
					case map[string]any:
						if text, ok := v["text"].(string); ok {
							texts = append(texts, text)
						}
						for k, x := range v {
							if k != "text" {
								collect(x)
							}
						}
					}
				}
				collect(body["input"])
				text := strings.Join(texts, "\n")
				observed <- text
				count, _ := codec.Count(text)
				if count > 24000 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"模拟上游：上下文超过24K测试阈值"}}`)
					return
				}
				compatCompleted(w, compatText())
			})
			for _, size := range []int{16000, 32000, 16000} {
				doc := "FACT_FIRST=first\n" + strings.Repeat(" a", size/2) + "\nFACT_MIDDLE=middle\n" + strings.Repeat(" a", size/2) + "\nFACT_LAST=last"
				payload := compatPayload(stream)
				payload["messages"] = []any{map[string]any{"role": "user", "content": doc}}
				resp := compatRequest(t, client, endpoint, payload, context.Background())
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if got := <-observed; got != doc {
					t.Fatal("长文在到达上游前被修改或截断")
				}
				if size > 24000 {
					if resp.StatusCode != 400 || !json.Valid(body) {
						t.Fatalf("容量拒绝未明确返回：status=%d", resp.StatusCode)
					}
					var failure struct {
						Error struct {
							Type    string `json:"type"`
							Message string `json:"message"`
						} `json:"error"`
					}
					if json.Unmarshal(body, &failure) != nil || failure.Error.Type != "invalid_request_error" || failure.Error.Message != "模拟上游：上下文超过24K测试阈值" {
						t.Fatalf("容量拒绝原因未保留：%s", body)
					}
				} else if resp.StatusCode != 200 {
					t.Fatalf("拒绝前后的小请求失败：%d %s", resp.StatusCode, body)
				}
				t.Logf("合成填充=%d stream=%t downstream=%d", size, stream, resp.StatusCode)
			}
			if calls.Load() != 3 {
				t.Fatalf("容量拒绝不应自动重试或追加调用：%d", calls.Load())
			}
		})
	}
}
