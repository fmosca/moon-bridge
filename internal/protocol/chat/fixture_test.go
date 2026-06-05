package chat

import (
	"encoding/json"
	"os"
	"testing"
)

func TestFixtureUnmarshal(t *testing.T) {
	data, err := os.ReadFile("/tmp/failing-request.json")
	if err != nil {
		t.Skip(err)
	}
	var req ChatRequest
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	for i, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			args := string(tc.Function.Arguments)
			if args == "{}" || args == `""` || args == "" {
				t.Logf("[%d] role=%s tool=%s args=%q", i, m.Role, tc.Function.Name, args)
			}
		}
	}
}
