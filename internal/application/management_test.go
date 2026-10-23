package application

import "testing"

func TestManagementArgumentsRejectInvalidRequests(t *testing.T) {
	for _, test := range []struct{ op, raw string }{
		{"create_application", `{"name":"test"}`},
		{"delete_application", `{"application_id":0}`},
		{"delete_application", `{"application_id":1,"value":"unexpected"}`},
		{"add_secret", `{"application_id":1,"name":"KEY","value":null}`},
		{"create_service", `{"application_id":1,"configuration":{"image_name":"nginx","privileged":true}}`},
		{"create_service", `{"application_id":1,"configuration":{"volume_mappings":[{"source":"x","arbitrary":true}]}}`},
		{"service_action", `{"application_id":1,"service_name":"../bad","action":"start"}`},
		{"service_action", `{"application_id":1,"service_name":"web","action":"exec"}`},
		{"service_action", `{"application_id":1,"service_name":"web","action":"start","parameters":{"args":["x"]}}`},
		{"service_action", `{"application_id":1,"service_name":"web","action":"run","parameters":{"env":{"PATH":"/tmp"}}}`},
		{"service_action", `{"application_id":1,"service_name":"web","action":"run","parameters":{"command":"sh"}}`},
		{"database_cluster_action", `{"action":"run"}`},
		{"create_database_user", `{"username":"user","password":"disposable","databases":null}`},
		{"list_applications", `null`},
	} {
		t.Run(test.op+test.raw, func(t *testing.T) {
			op, _ := ManagementOperationByName(test.op)
			if _, err := DecodeManagementArguments(op, []byte(test.raw)); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	op, _ := ManagementOperationByName("update_secret")
	args, err := DecodeManagementArguments(op, []byte(`{"application_id":1,"original_name":"OLD","name":"NEW","value":""}`))
	if err != nil || args.Value != "" {
		t.Fatal("empty environment values must be accepted")
	}
	op, _ = ManagementOperationByName("service_action")
	args, err = DecodeManagementArguments(op, []byte(`{"application_id":1,"service_name":"web","action":"run","parameters":{"args":["--confirm"],"env":{"NODE_ENV":"development"}}}`))
	if err != nil || args.Parameters == nil || args.Parameters.Env["NODE_ENV"] != "development" || len(args.Parameters.Args) != 1 {
		t.Fatalf("valid run parameters rejected: %+v %v", args, err)
	}
	if _, ok := ManagementOperationByName("service_action"); !ok {
		t.Fatal("service_action operation is missing")
	}
}
