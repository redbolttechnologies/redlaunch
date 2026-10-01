package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"redlaunch/internal/application"
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}

func schema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func (s *Server) tools() []tool {
	local := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	serviceProperties := func() map[string]any {
		return map[string]any{
			"application_id": map[string]any{"type": "integer", "minimum": 1, "description": "Numeric application ID from /applications/<id>."},
			"service_name":   map[string]any{"type": "string", "description": "Registered Compose service name, not a command or container name."},
		}
	}
	tools := []tool{
		{"read_guide", "Read a bundled Redlaunch integration guide. Use recipes for complete GHA image publishing, migration, and SSH deployment workflows; api-tokens for the machine API contract.", schema(map[string]any{"topic": map[string]any{"type": "string", "enum": []string{"api-tokens", "recipes", "ssh-keys", "installation", "reference", "mcp"}}}, "topic"), local},
		{"generate_run_workflow", "Generate a manual GitHub Actions workflow to run a registered service and poll completion. Uses vars.REDLAUNCH_URL and secrets.REDLAUNCH_RUN_TOKEN; does not dispatch anything. For SSH deployments read recipes.", schema(serviceProperties(), "application_id", "service_name"), local},
	}
	if s.client != nil {
		props := serviceProperties()
		props["job_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Job ID returned by dispatch. Jobs expire one hour after finishing and do not survive Redlaunch restarts."}
		tools = append(tools, tool{"get_run_status", "Poll an existing run using the configured application-scoped token. Returns only status; service output is omitted.", schema(props, "application_id", "service_name", "job_id"), map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}})
		if s.allowRun {
			tools = append(tools, tool{"run_service", "Start a registered one-off service using the configured token. Changes server state and may run migrations. Returns job_id; poll get_run_status. Concurrent duplicates reuse a running job, but retrying after completion starts another run.", schema(serviceProperties(), "application_id", "service_name"), map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true}})
		}
	}
	return tools
}

func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (any, *rpcError) {
	available := false
	for _, tool := range s.tools() {
		if tool.Name == name {
			available = true
			break
		}
	}
	if !available {
		return nil, failure(-32602, "Unknown or disabled tool")
	}
	if name == "read_guide" {
		var args struct {
			Topic string `json:"topic"`
		}
		if !decodeArguments(raw, &args) {
			return nil, failure(-32602, "Invalid guide arguments")
		}
		// Names only here; resource URIs are accepted exclusively by resources/read.
		for _, guide := range guides {
			if args.Topic == guide.Name {
				content, ok := readGuide(args.Topic)
				if !ok {
					return toolResult("Bundled guide could not be read", true), nil
				}
				return toolResult(content, false), nil
			}
		}
		return nil, failure(-32602, "Unknown guide topic")
	}
	var args struct {
		ApplicationID int64  `json:"application_id"`
		ServiceName   string `json:"service_name"`
		JobID         string `json:"job_id,omitempty"`
	}
	if !decodeArguments(raw, &args) || args.ApplicationID < 1 {
		return nil, failure(-32602, "A positive application_id and valid service_name are required")
	}
	serviceName, err := application.ValidateServiceName(args.ServiceName)
	if err != nil {
		return nil, failure(-32602, "Invalid service_name")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, present := fields["job_id"]; name != "get_run_status" && present {
		return nil, failure(-32602, "job_id is only accepted by get_run_status")
	}
	if name == "generate_run_workflow" {
		return toolResult(runWorkflow(args.ApplicationID, serviceName), false), nil
	}
	var result runResponse
	if name == "get_run_status" {
		if !validJobID(args.JobID) {
			return nil, failure(-32602, "Invalid job_id")
		}
		result, err = s.client.status(ctx, args.ApplicationID, serviceName, args.JobID)
	} else {
		result, err = s.client.run(ctx, args.ApplicationID, serviceName)
	}
	if err != nil {
		return toolResult(err.Error(), true), nil
	}
	content, err := json.Marshal(result)
	if err != nil {
		return toolResult("Could not encode Redlaunch result", true), nil
	}
	return toolResult(string(content), false), nil
}

func runWorkflow(applicationID int64, serviceName string) string {
	return fmt.Sprintf(`# Save as .github/workflows/redlaunch-run.yml.
# Set vars.REDLAUNCH_URL to a managed HTTPS origin. For loopback HTTP,
# establish an SSH tunnel on the runner first (see the recipes guide).
# Set secrets.REDLAUNCH_RUN_TOKEN to a token scoped to application %d.
name: Redlaunch service run

on:
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: redlaunch-run-%d-%s
  cancel-in-progress: false

jobs:
  run:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      REDLAUNCH_URL: ${{ vars.REDLAUNCH_URL }}
      REDLAUNCH_RUN_TOKEN: ${{ secrets.REDLAUNCH_RUN_TOKEN }}
      APPLICATION_ID: "%d"
      SERVICE_NAME: "%s"
    steps:
      - name: Dispatch and wait for completion
        shell: bash
        run: |
          set -euo pipefail
          : "${REDLAUNCH_URL:?Set vars.REDLAUNCH_URL}"
          : "${REDLAUNCH_RUN_TOKEN:?Set secrets.REDLAUNCH_RUN_TOKEN}"
          python3 - <<'PY'
          import ipaddress, os, urllib.parse
          url = urllib.parse.urlsplit(os.environ["REDLAUNCH_URL"])
          valid = url.scheme == "https"
          if url.scheme == "http":
              try:
                  valid = ipaddress.ip_address(url.hostname).is_loopback
              except ValueError:
                  valid = False
          if not valid or not url.hostname or url.username is not None or url.password is not None or url.path not in ("", "/") or url.query or url.fragment:
              raise SystemExit("REDLAUNCH_URL must be an HTTPS origin or loopback HTTP origin")
          PY
          endpoint="${REDLAUNCH_URL%%/}/api/v1/applications/$APPLICATION_ID/services/$SERVICE_NAME/run"
          # Do not retry dispatch automatically: a lost response may follow a successful run.
          response=$(curl --fail --silent --show-error --max-time 30 -X POST \
            -H "Authorization: Bearer $REDLAUNCH_RUN_TOKEN" "$endpoint")
          job_id=$(python3 -c 'import json,re,sys; value=json.load(sys.stdin)["job_id"]; assert re.fullmatch(r"[A-Za-z0-9_-]{1,128}",value); print(value)' <<< "$response")
          for _ in $(seq 1 60); do
            response=$(curl --fail --silent --show-error --max-time 10 \
              -H "Authorization: Bearer $REDLAUNCH_RUN_TOKEN" "$endpoint/status?id=$job_id")
            status=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])' <<< "$response")
            case "$status" in
              complete) exit 0 ;;
              failed) echo "Redlaunch service run failed; inspect it in Redlaunch" >&2; exit 1 ;;
              running) sleep 2 ;;
              *) echo "Unexpected Redlaunch job status" >&2; exit 1 ;;
            esac
          done
          echo "Timed out waiting for Redlaunch; the server job may still be running" >&2
          exit 1
`, applicationID, applicationID, serviceName, applicationID, serviceName)
}
