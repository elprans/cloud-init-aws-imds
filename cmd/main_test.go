package main

import (
	"encoding/json"
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
					"instance-profile": map[string]interface{}{
						"arn": "arn:aws:iam::123456789012:instance-profile/test-profile",
						"id":  "AIPA1234567890",
					},
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

// --- Token endpoint tests ---

func TestTokenHandlerPutWithTTL(t *testing.T) {
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	w := httptest.NewRecorder()
	tokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Error("expected non-empty token")
	}
}

func TestTokenHandlerRejectsGET(t *testing.T) {
	req := httptest.NewRequest("GET", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	w := httptest.NewRecorder()
	tokenHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsMissingTTL(t *testing.T) {
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	w := httptest.NewRecorder()
	tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsNonNumericTTL(t *testing.T) {
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "abc")
	w := httptest.NewRecorder()
	tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()
	tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- Middleware tests (Server header, Content-Type, method enforcement) ---

func TestMiddlewareServerHeader(t *testing.T) {
	handler := logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	req := httptest.NewRequest("GET", "/latest/meta-data/instance-id", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Server"); got != "EC2ws" {
		t.Errorf("expected Server: EC2ws, got %q", got)
	}
}

func TestMiddlewareContentType(t *testing.T) {
	handler := logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	req := httptest.NewRequest("GET", "/latest/meta-data/instance-id", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("expected Content-Type: text/plain, got %q", got)
	}
}

func TestMiddlewareRejectsPostOnMetadata(t *testing.T) {
	handler := logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	req := httptest.NewRequest("POST", "/latest/meta-data/instance-id", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST on metadata, got %d", w.Code)
	}
}

func TestMiddlewareAllowsPutOnToken(t *testing.T) {
	handler := logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for PUT on token endpoint, got %d", w.Code)
	}
}

// --- IAM info tests ---

func TestIamInfoHandler(t *testing.T) {
	setTestData(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/info", nil)
	w := httptest.NewRecorder()
	iamInfoHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var info iamInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if info.Code != "Success" {
		t.Errorf("expected Code=Success, got %q", info.Code)
	}
	if info.InstanceProfileArn != "arn:aws:iam::123456789012:instance-profile/test-profile" {
		t.Errorf("unexpected InstanceProfileArn: %q", info.InstanceProfileArn)
	}
	if info.InstanceProfileID != "AIPA1234567890" {
		t.Errorf("unexpected InstanceProfileId: %q", info.InstanceProfileID)
	}
	if info.LastUpdated == "" {
		t.Error("expected non-empty LastUpdated")
	}
}

func TestIamInfoHandlerNoIAM(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "iam")
	setTestData(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/info", nil)
	w := httptest.NewRecorder()
	iamInfoHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- Instance identity document tests ---

func TestInstanceIdentityDocumentNullFields(t *testing.T) {
	setTestData(t, baseTestData())

	// We cannot call instanceIdentityHandler directly because it needs a
	// network interface. Instead, test the struct marshaling behavior.
	doc := instanceIdentityDocument{
		DevpayProductCodes:      nil,
		MarketplaceProductCodes: nil,
		AvailabilityZone:        "us-west-2a",
		PrivateIP:               "10.0.0.1",
		Version:                 "2017-09-30",
		InstanceID:              "i-test-1234",
		BillingProducts:         nil,
		InstanceType:            "m7g.metal-48xl",
		AccountID:               "123456789012",
		ImageID:                 "debian bookworm",
		PendingTime:             "2025-01-01T00:00:00Z",
		Architecture:            "aarch64",
		KernelID:                nil,
		RamdiskID:               nil,
		Region:                  "us-west-2",
	}

	jsonData, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	body := string(jsonData)

	// devpayProductCodes and marketplaceProductCodes must be null, not []
	if strings.Contains(body, `"devpayProductCodes":[]`) {
		t.Error("devpayProductCodes should be null, not empty array")
	}
	if !strings.Contains(body, `"devpayProductCodes":null`) {
		t.Error("devpayProductCodes should be null")
	}
	if !strings.Contains(body, `"marketplaceProductCodes":null`) {
		t.Error("marketplaceProductCodes should be null")
	}

	// pendingTime must be a timestamp string, not null
	if strings.Contains(body, `"pendingTime":null`) {
		t.Error("pendingTime should be a timestamp string, not null")
	}
	if !strings.Contains(body, `"pendingTime":"2025-01-01T00:00:00Z"`) {
		t.Errorf("unexpected pendingTime in: %s", body)
	}

	// kernelId and ramdiskId should be null
	if !strings.Contains(body, `"kernelId":null`) {
		t.Error("kernelId should be null")
	}
	if !strings.Contains(body, `"ramdiskId":null`) {
		t.Error("ramdiskId should be null")
	}
}

func TestInstanceIdentityDocumentFieldOrder(t *testing.T) {
	doc := instanceIdentityDocument{
		AvailabilityZone: "us-west-2a",
		PrivateIP:        "10.0.0.1",
		Version:          "2017-09-30",
		InstanceID:       "i-test",
		InstanceType:     "t2.micro",
		AccountID:        "123",
		ImageID:          "ami-test",
		PendingTime:      "2025-01-01T00:00:00Z",
		Architecture:     "x86_64",
		Region:           "us-west-2",
	}

	jsonData, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Verify deterministic ordering: devpayProductCodes comes before availabilityZone
	body := string(jsonData)
	devIdx := strings.Index(body, "devpayProductCodes")
	azIdx := strings.Index(body, "availabilityZone")
	if devIdx == -1 || azIdx == -1 {
		t.Fatalf("missing expected fields in: %s", body)
	}
	if devIdx >= azIdx {
		t.Error("devpayProductCodes should appear before availabilityZone (struct field order)")
	}
}
