package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// ManagementOperation is the shared machine API and MCP request contract.
// Execution belongs to application services; these definitions grant no access.
type ManagementOperation struct {
	Name        string
	Description string
	ReadOnly    bool
	Properties  map[string]any
	Required    []string
}

type ManagementArguments struct {
	ApplicationID int64                   `json:"application_id"`
	DomainID      int64                   `json:"domain_id"`
	RoutingID     int64                   `json:"routing_id"`
	ServiceName   string                  `json:"service_name"`
	Name          string                  `json:"name"`
	FolderName    string                  `json:"folder_name"`
	OriginalName  string                  `json:"original_name"`
	Value         string                  `json:"value"`
	Owner         string                  `json:"owner"`
	Username      string                  `json:"username"`
	Password      string                  `json:"password"`
	Databases     []string                `json:"databases"`
	Configuration ApplicationServiceInput `json:"configuration"`
	Routing       RoutingInput            `json:"routing"`
	Provider      string                  `json:"provider"`
	Version       string                  `json:"version"`
	DefaultUser   string                  `json:"default_user"`
	Action        string                  `json:"action"`
}

// ManagementOperations lists only operations supported by the service layer.
func ManagementOperations() []ManagementOperation {
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "integer", "minimum": 1}
	properties := map[string]any{
		"application_id": id, "domain_id": id, "routing_id": id,
		"name": str, "folder_name": str, "service_name": str, "original_name": str,
		"value": str, "owner": str, "username": str, "password": str,
		"provider": str, "version": str, "default_user": str,
		"databases":     map[string]any{"type": "array", "items": str},
		"configuration": managementStructSchema(reflect.TypeFor[ApplicationServiceInput]()),
		"routing":       managementStructSchema(reflect.TypeFor[RoutingInput]()),
		"action":        map[string]any{"type": "string", "enum": []string{"start", "stop", "restart", "run"}},
	}
	var result []ManagementOperation
	add := func(name, description string, read bool, required []string, optional ...string) {
		props := map[string]any{}
		for _, key := range append(append([]string{}, required...), optional...) {
			props[key] = properties[key]
		}
		result = append(result, ManagementOperation{name, description, read, props, required})
	}
	add("list_applications", "List managed applications.", true, nil)
	add("get_application", "Read application metadata.", true, []string{"application_id"})
	add("create_application", "Create an application directory and registration.", false, []string{"name", "folder_name"})
	add("rename_application", "Rename an application display name.", false, []string{"application_id", "name"})
	add("delete_application", "Delete an application and its managed resources and files.", false, []string{"application_id"})
	add("list_services", "List registered application services and runtime status.", true, []string{"application_id"})
	add("create_service", "Create a custom managed application service.", false, []string{"application_id", "configuration"})
	add("update_service", "Replace a custom application service configuration.", false, []string{"application_id", "service_name", "configuration"})
	add("delete_service", "Delete a service and its routing.", false, []string{"application_id", "service_name"})
	add("service_action", "Start, stop, restart, or run a registered service once.", false, []string{"application_id", "service_name", "action"})
	add("list_environment", "List environment names only; values are never returned.", true, []string{"application_id"})
	for _, kind := range []string{"variable", "secret"} {
		add("add_"+kind, "Add an application "+kind+". Values are not echoed.", false, []string{"application_id", "name", "value"})
		add("update_"+kind, "Update or rename an application "+kind+". Values are not echoed.", false, []string{"application_id", "original_name", "name", "value"})
		add("delete_"+kind, "Delete an application "+kind+".", false, []string{"application_id", "name"})
	}
	add("list_domains", "List application domains.", true, []string{"application_id"})
	add("create_domain", "Register an application domain.", false, []string{"application_id", "name"})
	add("delete_domain", "Delete a domain and its routings.", false, []string{"application_id", "name"})
	add("list_routings", "List routings for an application domain.", true, []string{"application_id", "domain_id"})
	add("create_routing", "Create domain routing to an application service.", false, []string{"application_id", "domain_id", "routing"})
	add("update_routing", "Replace a domain routing.", false, []string{"application_id", "domain_id", "routing_id", "routing"})
	add("delete_routing", "Delete a domain routing.", false, []string{"application_id", "domain_id", "routing_id"})
	add("get_database_cluster", "Read managed database cluster metadata.", true, nil)
	add("enable_database_cluster", "Enable the managed PostgreSQL cluster.", false, []string{"version", "default_user", "password"}, "provider")
	add("disable_database_cluster", "Disable the managed database cluster.", false, nil)
	add("database_cluster_action", "Start, stop, or restart the managed cluster.", false, []string{"action"})
	result[len(result)-1].Properties["action"] = map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"}}
	add("list_databases", "List managed databases.", true, nil)
	add("create_database", "Create a database owned by an existing user. Create the user first; no generated credentials are returned.", false, []string{"name", "owner"})
	add("delete_database", "Drop a managed database and its data.", false, []string{"name"})
	add("list_database_users", "List managed database users and permissions.", true, nil)
	add("create_database_user", "Create a database user with supplied credentials and database access.", false, []string{"username", "password", "databases"})
	add("update_database_user_password", "Rotate a managed user's password.", false, []string{"username", "password"})
	add("set_database_user_permissions", "Replace a managed user's database access list.", false, []string{"username", "databases"})
	add("delete_database_user", "Delete a managed database user.", false, []string{"username"})
	return result
}

func ManagementOperationByName(name string) (ManagementOperation, bool) {
	for _, op := range ManagementOperations() {
		if op.Name == name {
			return op, true
		}
	}
	return ManagementOperation{}, false
}

// DecodeManagementArguments rejects unknown, missing, null, and mistyped fields
// before scheduling work. Service methods own domain and path validation.
func DecodeManagementArguments(op ManagementOperation, raw []byte) (ManagementArguments, error) {
	var args ManagementArguments
	invalid := errors.New("invalid management arguments")
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return args, invalid
	}
	for key, value := range fields {
		if _, ok := op.Properties[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, invalid
		}
	}
	for _, key := range op.Required {
		v, ok := fields[key]
		if !ok {
			return args, invalid
		}
		if key != "value" && op.Properties[key].(map[string]any)["type"] == "string" {
			var text string
			if json.Unmarshal(v, &text) != nil || strings.TrimSpace(text) == "" {
				return args, invalid
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&args) != nil || dec.Decode(new(any)) != io.EOF {
		return args, invalid
	}
	for _, key := range []string{"application_id", "domain_id", "routing_id"} {
		if v, ok := fields[key]; ok {
			var id int64
			if json.Unmarshal(v, &id) != nil || id < 1 {
				return args, invalid
			}
		}
	}
	if _, ok := fields["service_name"]; ok {
		if _, err := ValidateServiceName(args.ServiceName); err != nil {
			return args, invalid
		}
	}
	if _, ok := fields["action"]; ok && args.Action != "start" && args.Action != "stop" && args.Action != "restart" && args.Action != "run" {
		return args, invalid
	}
	if op.Name == "database_cluster_action" && args.Action == "run" {
		return args, invalid
	}
	return args, nil
}

func managementStructSchema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int:
		return map[string]any{"type": "integer"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": managementStructSchema(t.Elem())}
	default:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			props[f.Tag.Get("json")] = managementStructSchema(f.Type)
		}
		return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	}
}
