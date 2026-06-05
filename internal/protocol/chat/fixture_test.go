package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestGuardsOnFixture(t *testing.T) {
	data, err := os.ReadFile("/tmp/failing-request.json")
	if err != nil {
		t.Skip(err)
	}
	var req ChatRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}

	// Check every message for tool calls
	tcTotal := 0
	for i, m := range req.Messages {
		if len(m.ToolCalls) > 0 {
			tcTotal += len(m.ToolCalls)
			first := m.ToolCalls[0]
			args := string(first.Function.Arguments)
			isEmpty := args == "{}" || args == `""` || args == ""
			if i >= 120 || isEmpty {
				t.Logf("[%d] tool=%s args_len=%d empty=%v", i, first.Function.Name, len(args), isEmpty)
			}
		}
	}
	t.Logf("total tc=%d across %d msgs", tcTotal, len(req.Messages))
	fmt.Printf("FIXTURE: %d msgs, %d tcs\n", len(req.Messages), tcTotal)
}
