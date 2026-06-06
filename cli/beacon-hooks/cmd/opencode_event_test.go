package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestOpenCodeEventRecordsPrompt(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	out := runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "chat.message",
		"session_id": "session-1",
		"directory":  "/tmp/project",
		"model":      "anthropic/claude-sonnet-4",
		"output": map[string]interface{}{
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "text": "summarize token=opencode-secret"},
			},
		},
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("event.action = %q, want prompt.submitted", action)
	}
	if harness := event["harness"].(map[string]interface{})["name"]; harness != "opencode" {
		t.Fatalf("harness.name = %q, want opencode", harness)
	}
	if got := event["prompt"].(map[string]interface{})["text"]; got != "summarize token=[REDACTED]" {
		t.Fatalf("prompt.text = %q, want redacted prompt", got)
	}
	if got := event["model"]; got != "anthropic/claude-sonnet-4" {
		t.Fatalf("model = %q, want opencode model", got)
	}
}

func TestOpenCodeEventFixtureRecordsPrompt(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "full")

	input := readOpenCodeFixture(t, "chat_message.json")
	runHookWithInput(t, runOpenCodeEvent, input)
	event := lastEndpointEvent(t, logPath)
	if action := event["event"].(map[string]interface{})["action"]; action != "prompt.submitted" {
		t.Fatalf("fixture event.action = %q, want prompt.submitted", action)
	}
}

func TestOpenCodeEventMetadataOmitsPromptAndRawPayload(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)
	t.Setenv("BEACON_CONTENT_RETENTION", "metadata")

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "chat.message",
		"session_id": "session-1",
		"output": map[string]interface{}{
			"parts": []interface{}{
				map[string]interface{}{"type": "text", "text": "secret prompt"},
			},
		},
	})
	event := lastEndpointEvent(t, logPath)
	if _, ok := event["prompt"]; ok {
		t.Fatalf("metadata retention should omit prompt: %#v", event["prompt"])
	}
	raw := event["raw"].(map[string]interface{})
	if _, ok := raw["opencode"]; ok {
		t.Fatalf("metadata retention should omit raw opencode payload: %#v", raw)
	}
}

func TestOpenCodeEventMapsPermissionDecision(t *testing.T) {
	fields := map[string]interface{}{
		"type":       "permission.replied",
		"session_id": "session-1",
		"tool":       "bash",
		"decision":   "accepted",
	}
	_, category, _, _, eventFields := opencodeEndpointEvent(fields, "session-1")
	if category != "approval" {
		t.Fatalf("category = %q, want approval", category)
	}
	approval := eventFields["approval"].(map[string]interface{})
	if approval["decision"] != "accepted" {
		t.Fatalf("approval.decision = %q, want accepted", approval["decision"])
	}
}

func TestOpenCodeEventIgnoresUnsupportedEvents(t *testing.T) {
	setupHookConfigDirs(t)
	platformFlag = "opencode"
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	t.Setenv("BEACON_ENDPOINT_MODE", "1")
	t.Setenv("BEACON_ENDPOINT_LOG", logPath)

	out := runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "message.updated",
		"session_id": "session-1",
	})
	if len(out) != 0 {
		t.Fatalf("response = %#v, want empty object", out)
	}
	assertNoEndpointLog(t, logPath)

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "tool.completed",
		"session_id": "session-1",
	})
	assertNoEndpointLog(t, logPath)

	runHookWithInput(t, runOpenCodeEvent, map[string]interface{}{
		"type":       "session.deleted",
		"session_id": "session-1",
	})
	assertNoEndpointLog(t, logPath)
}

func TestOpenCodeForwardedEventsMatchGoIngestion(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "beacon", "internal", "endpoint", "hooks", "assets", "opencode", "beacon.ts"))
	if err != nil {
		t.Fatalf("read embedded plugin source: %v", err)
	}
	got := forwardedEventsFromPluginSource(t, string(source))
	want := supportedOpenCodeEventTypes()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forwarded events do not match Go ingestion\nplugin=%#v\ngo=%#v", got, want)
	}
}

func assertNoEndpointLog(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("endpoint log should not exist for unsupported event: %s", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected endpoint log stat error: %v", err)
	}
}

func forwardedEventsFromPluginSource(t *testing.T, source string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)forwardedEvents = new Set\(\[(.*?)\]\)`)
	match := re.FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatalf("forwardedEvents list not found in plugin source")
	}
	itemRE := regexp.MustCompile(`"([^"]+)"`)
	matches := itemRE.FindAllStringSubmatch(match[1], -1)
	events := []string{"chat.message"}
	for _, match := range matches {
		events = append(events, match[1])
	}
	return events
}

func readOpenCodeFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "opencode", name))
	if err != nil {
		t.Fatalf("read opencode fixture %s: %v", name, err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode opencode fixture %s: %v", name, err)
	}
	return payload
}
