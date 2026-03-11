package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockInstanceData implements InstanceDataSource backed by an in-memory map.
type mockInstanceData struct {
	data map[string]interface{}
}

func (m *mockInstanceData) GetInstanceData() (map[string]interface{}, error) {
	return m.data, nil
}

func setTestData(t *testing.T, data map[string]interface{}) {
	t.Helper()
	orig := dataSource
	t.Cleanup(func() { dataSource = orig })
	dataSource = &mockInstanceData{data: data}
}

func baseTestData() map[string]interface{} {
	return map[string]interface{}{
		"v1": map[string]interface{}{
			"instance_id":       "i-test-1234",
			"region":            "us-west-2",
			"availability_zone": "us-west-2a",
			"machine":           "aarch64",
			"distro":            "debian",
			"distro_release":    "bookworm",
		},
		"ds": map[string]interface{}{
			"meta_data": map[string]interface{}{
				"local_hostname": "test-host",
				"instance_type":  "m7g.metal-48xl",
				"tags": map[string]interface{}{
					"aws:autoscaling:groupName": "test-asg",
					"Name":                      "test-instance",
				},
				"autoscaling": map[string]interface{}{
					"target_lifecycle_state": "InService",
				},
				"iam": map[string]interface{}{
					"role-name": "test-role",
					"credentials": map[string]interface{}{
						"AccessKeyId":     "AKIATEST",
						"SecretAccessKey": "secret",
						"Token":           "tok",
						"Code":            "Success",
						"Expiration":      "2099-01-01T00:00:00Z",
						"LastUpdated":     "2025-01-01T00:00:00Z",
						"Type":            "AWS-HMAC",
					},
				},
				"services": map[string]interface{}{
					"domain":    "amazonaws.com",
					"endpoints": map[string]interface{}{},
				},
			},
		},
	}
}

func TestTagsInstanceList(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "aws:autoscaling:groupName") {
		t.Errorf("missing key 'aws:autoscaling:groupName' in: %s", body)
	}
	if !strings.Contains(body, "Name") {
		t.Errorf("missing key 'Name' in: %s", body)
	}
}

func TestTagsInstanceListNoTrailingSlash(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestTagsInstanceGetASG(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/aws:autoscaling:groupName", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "test-asg" {
		t.Errorf("expected 'test-asg', got %q", w.Body.String())
	}
}

func TestTagsInstanceGetName(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/Name", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "test-instance" {
		t.Errorf("expected 'test-instance', got %q", w.Body.String())
	}
}

func TestTagsInstanceMissingKey(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/nonexistent", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestTagsInstanceNoTagsSection(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "tags")
	setTestData(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/", nil)
	w := httptest.NewRecorder()
	tagsInstanceHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when tags section missing, got %d", w.Code)
	}
}

func TestAutoscalingLifecycleState(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/autoscaling/target-lifecycle-state", nil)
	w := httptest.NewRecorder()
	autoscalingLifecycleStateHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "InService" {
		t.Errorf("expected 'InService', got %q", w.Body.String())
	}
}

func TestAutoscalingLifecycleStateCustomValue(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["autoscaling"] = map[string]interface{}{
		"target_lifecycle_state": "Pending:Wait",
	}
	setTestData(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/autoscaling/target-lifecycle-state", nil)
	w := httptest.NewRecorder()
	autoscalingLifecycleStateHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "Pending:Wait" {
		t.Errorf("expected 'Pending:Wait', got %q", w.Body.String())
	}
}

func TestAutoscalingLifecycleStateDefault(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "autoscaling")
	setTestData(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/autoscaling/target-lifecycle-state", nil)
	w := httptest.NewRecorder()
	autoscalingLifecycleStateHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "InService" {
		t.Errorf("expected default 'InService', got %q", w.Body.String())
	}
}
