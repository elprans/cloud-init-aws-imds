package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/credentials"
)

// mockInstanceData implements InstanceDataSource backed by an in-memory map.
type mockInstanceData struct {
	data map[string]interface{}
}

func (m *mockInstanceData) GetInstanceData() (map[string]interface{}, error) {
	return m.data, nil
}

// mockNetworkInfo provides a deterministic NetworkInfo for tests.
type mockNetworkInfo struct {
	ifaces []net.Interface
	addrs  map[string][]net.Addr
}

func (m *mockNetworkInfo) InterfaceByName(name string) (*net.Interface, error) {
	for i := range m.ifaces {
		if m.ifaces[i].Name == name {
			return &m.ifaces[i], nil
		}
	}
	return nil, &net.OpError{Op: "route", Net: "ip+net", Err: &net.AddrError{Err: "no such network interface", Addr: name}}
}

func (m *mockNetworkInfo) Interfaces() ([]net.Interface, error) {
	return m.ifaces, nil
}

func (m *mockNetworkInfo) InterfaceAddrs(iface *net.Interface) ([]net.Addr, error) {
	if addrs, ok := m.addrs[iface.Name]; ok {
		return addrs, nil
	}
	return nil, nil
}

// mockBlockDeviceSource provides deterministic block device data for tests.
type mockBlockDeviceSource struct {
	devices map[string]string
}

func (m *mockBlockDeviceSource) GetBlockDevices() (map[string]string, error) {
	return m.devices, nil
}

func defaultMockNetworkInfo() *mockNetworkInfo {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	return &mockNetworkInfo{
		ifaces: []net.Interface{
			{
				Index:        1,
				Name:         "eth0",
				HardwareAddr: mac,
				Flags:        net.FlagUp,
			},
		},
		addrs: map[string][]net.Addr{
			"eth0": {
				&net.IPNet{
					IP:   net.ParseIP("10.0.0.42"),
					Mask: net.CIDRMask(24, 32),
				},
			},
		},
	}
}

func newTestServer(t *testing.T, data map[string]interface{}) *Server {
	t.Helper()
	return &Server{
		dataSource:   &mockInstanceData{data: data},
		startTime:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		options:      &Options{NetIface: "eth0", AccountID: "123456789012"},
		networkInfo:  defaultMockNetworkInfo(),
		blockDevices: &mockBlockDeviceSource{devices: map[string]string{}},
	}
}

func newTestServerWithIAM(t *testing.T, data map[string]interface{}) *Server {
	t.Helper()
	s := newTestServer(t, data)
	s.iamCreds = credentials.NewStaticCredentials("AKIATEST", "secret", "tok")
	s.imdsCreds = &IMDSCredentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		Token:           "tok",
		Code:            "Success",
		Expiration:      "2099-01-01T00:00:00Z",
		LastUpdated:     "2025-01-01T00:00:00Z",
		Type:            "AWS-HMAC",
	}
	return s
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

// =========================================================================
// Tests below cover edge cases, error paths, and behavior the SDK client
// cannot exercise (invalid requests, missing data, struct marshaling).
// Happy-path tests for all endpoints live in sdk_compat_test.go.
// =========================================================================

// --- Token endpoint edge cases ---

func TestTokenHandlerRejectsGET(t *testing.T) {
	s := newTestServer(t, baseTestData())
	req := httptest.NewRequest("GET", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	w := httptest.NewRecorder()
	s.tokenHandler(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsMissingTTL(t *testing.T) {
	s := newTestServer(t, baseTestData())
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	w := httptest.NewRecorder()
	s.tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsNonNumericTTL(t *testing.T) {
	s := newTestServer(t, baseTestData())
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "abc")
	w := httptest.NewRecorder()
	s.tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandlerRejectsXForwardedFor(t *testing.T) {
	s := newTestServer(t, baseTestData())
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	w := httptest.NewRecorder()
	s.tokenHandler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestTokenHandlerReturnsTTLHeader(t *testing.T) {
	s := newTestServer(t, baseTestData())
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "300")
	w := httptest.NewRecorder()
	s.tokenHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ttl := w.Header().Get("X-Aws-Ec2-Metadata-Token-Ttl-Seconds")
	if ttl != "300" {
		t.Errorf("expected TTL header '300', got %q", ttl)
	}
}

// --- Middleware edge cases ---

func TestMiddlewareServerHeader(t *testing.T) {
	s := newTestServer(t, baseTestData())
	handler := s.logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	s := newTestServer(t, baseTestData())
	handler := s.logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	s := newTestServer(t, baseTestData())
	handler := s.logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	s := newTestServer(t, baseTestData())
	handler := s.logRequest(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok")) //nolint:errcheck
	}))
	req := httptest.NewRequest("PUT", "/latest/api/token", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for PUT on token endpoint, got %d", w.Code)
	}
}

// --- Instance identity document struct marshaling ---

func TestInstanceIdentityDocumentNullFields(t *testing.T) {
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

	if strings.Contains(body, `"devpayProductCodes":[]`) {
		t.Error("devpayProductCodes should be null, not empty array")
	}
	if !strings.Contains(body, `"devpayProductCodes":null`) {
		t.Error("devpayProductCodes should be null")
	}
	if !strings.Contains(body, `"marketplaceProductCodes":null`) {
		t.Error("marketplaceProductCodes should be null")
	}
	if !strings.Contains(body, `"pendingTime":"2025-01-01T00:00:00Z"`) {
		t.Errorf("unexpected pendingTime in: %s", body)
	}
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

// --- instance-type default value ---

func TestInstanceTypeHandlerDefault(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "instance_type")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/instance-type", nil)
	w := httptest.NewRecorder()
	s.instanceTypeHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "t2.micro" {
		t.Errorf("expected default 't2.micro', got %q", w.Body.String())
	}
}

// --- local-ipv4 error path ---

func TestLocalIPv4HandlerBadInterface(t *testing.T) {
	s := newTestServer(t, baseTestData())
	s.options.NetIface = "nonexistent-iface-999"

	req := httptest.NewRequest("GET", "/latest/meta-data/local-ipv4", nil)
	w := httptest.NewRecorder()
	s.localIPv4Handler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- mac error path ---

func TestMacHandlerBadInterface(t *testing.T) {
	s := newTestServer(t, baseTestData())
	s.options.NetIface = "nonexistent-iface-999"

	req := httptest.NewRequest("GET", "/latest/meta-data/mac", nil)
	w := httptest.NewRecorder()
	s.macHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// --- macs virtual interface filtering ---

func TestMacsHandlerFiltersVirtual(t *testing.T) {
	mac1, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	mac2, _ := net.ParseMAC("11:22:33:44:55:66")
	s := newTestServer(t, baseTestData())
	s.networkInfo = &mockNetworkInfo{
		ifaces: []net.Interface{
			{Index: 1, Name: "eth0", HardwareAddr: mac1, Flags: net.FlagUp},
			{Index: 2, Name: "lo0"},
			{Index: 3, Name: "docker0", HardwareAddr: mac2, Flags: net.FlagUp},
			{Index: 4, Name: "veth123", HardwareAddr: mac2, Flags: net.FlagUp},
		},
		addrs: map[string][]net.Addr{},
	}

	req := httptest.NewRequest("GET", "/latest/meta-data/network/interfaces/macs", nil)
	w := httptest.NewRecorder()
	s.macsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "aa:bb:cc:dd:ee:ff/" {
		t.Errorf("expected only eth0 MAC, got %q", w.Body.String())
	}
}

// --- block-device-mapping not found ---

func TestBlockDeviceMappingHandlerNotFound(t *testing.T) {
	s := newTestServer(t, baseTestData())
	s.blockDevices = &mockBlockDeviceSource{
		devices: map[string]string{"root": "/dev/sda1"},
	}

	req := httptest.NewRequest("GET", "/latest/meta-data/block-device-mapping/nosuch", nil)
	w := httptest.NewRecorder()
	s.blockDeviceMappingHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- Tags error paths ---

func TestTagsInstanceMissingKey(t *testing.T) {
	s := newTestServer(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/nonexistent", nil)
	w := httptest.NewRecorder()
	s.tagsInstanceHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestTagsInstanceNoTagsSection(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "tags")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/tags/instance/", nil)
	w := httptest.NewRecorder()
	s.tagsInstanceHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when tags section missing, got %d", w.Code)
	}
}

// --- Autoscaling edge cases ---

func TestAutoscalingLifecycleStateCustomValue(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["autoscaling"] = map[string]interface{}{
		"target_lifecycle_state": "Pending:Wait",
	}
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/autoscaling/target-lifecycle-state", nil)
	w := httptest.NewRecorder()
	s.autoscalingLifecycleStateHandler(w, req)

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
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/autoscaling/target-lifecycle-state", nil)
	w := httptest.NewRecorder()
	s.autoscalingLifecycleStateHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "InService" {
		t.Errorf("expected default 'InService', got %q", w.Body.String())
	}
}

// --- IAM info error path ---

func TestIamInfoHandlerNoIAM(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "iam")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/info", nil)
	w := httptest.NewRecorder()
	s.iamInfoHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- IAM security-credentials error paths ---

func TestIamSecurityCredentialsListHandlerNoIAM(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "iam")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/security-credentials", nil)
	w := httptest.NewRecorder()
	s.iamSecurityCredentialsListHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestIamSecurityCredentialsHandlerTrailingSlashListsFallback(t *testing.T) {
	s := newTestServer(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/security-credentials/", nil)
	w := httptest.NewRecorder()
	s.iamSecurityCredentialsHandler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != "test-role" {
		t.Errorf("expected 'test-role', got %q", w.Body.String())
	}
}

func TestIamSecurityCredentialsHandlerWrongRole(t *testing.T) {
	s := newTestServerWithIAM(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/security-credentials/wrong-role", nil)
	w := httptest.NewRecorder()
	s.iamSecurityCredentialsHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wrong role, got %d", w.Code)
	}
}

func TestIamSecurityCredentialsHandlerNoCredentials(t *testing.T) {
	s := newTestServer(t, baseTestData())

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/security-credentials/test-role", nil)
	w := httptest.NewRecorder()
	s.iamSecurityCredentialsHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when no credentials, got %d", w.Code)
	}
}

func TestIamSecurityCredentialsHandlerNoIAM(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "iam")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/iam/security-credentials/test-role", nil)
	w := httptest.NewRecorder()
	s.iamSecurityCredentialsHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- services/domain error paths ---

func TestServicesDomainHandlerNoServices(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "services")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/domain", nil)
	w := httptest.NewRecorder()
	s.servicesDomainHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestServicesDomainHandlerEmptyServices(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = nil
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/domain", nil)
	w := httptest.NewRecorder()
	s.servicesDomainHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- services/endpoints error paths ---

func TestServicesEndpointsHandlerNoServices(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	delete(md, "services")
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/endpoints", nil)
	w := httptest.NewRecorder()
	s.servicesEndpointsHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestServicesEndpointsHandlerNullServices(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = nil
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/endpoints", nil)
	w := httptest.NewRecorder()
	s.servicesEndpointsHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestServicesEndpointsHandlerNoEndpoints(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = map[string]interface{}{
		"domain": "amazonaws.com",
	}
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/endpoints", nil)
	w := httptest.NewRecorder()
	s.servicesEndpointsHandler(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestServicesEndpointsHandlerNullEndpoints(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = map[string]interface{}{
		"domain":    "amazonaws.com",
		"endpoints": nil,
	}
	s := newTestServer(t, data)

	req := httptest.NewRequest("GET", "/latest/meta-data/services/endpoints", nil)
	w := httptest.NewRecorder()
	s.servicesEndpointsHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- getEndpoints helper (used by getAWSConfig, not handlers) ---

func TestGetEndpoints(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = map[string]interface{}{
		"domain": "amazonaws.com",
		"endpoints": map[string]interface{}{
			"sts": "https://sts.us-west-2.amazonaws.com",
			"ec2": "https://ec2.us-west-2.amazonaws.com",
		},
	}
	s := newTestServer(t, data)

	eps, err := s.getEndpoints()
	if err != nil {
		t.Fatalf("getEndpoints failed: %v", err)
	}
	if eps["sts"] != "https://sts.us-west-2.amazonaws.com" {
		t.Errorf("unexpected sts: %v", eps["sts"])
	}
	if eps["ec2"] != "https://ec2.us-west-2.amazonaws.com" {
		t.Errorf("unexpected ec2: %v", eps["ec2"])
	}
}

func TestGetEndpointsNoServices(t *testing.T) {
	data := baseTestData()
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = nil
	s := newTestServer(t, data)

	eps, err := s.getEndpoints()
	if err != nil {
		t.Fatalf("getEndpoints failed: %v", err)
	}
	if eps != nil {
		t.Errorf("expected nil, got %v", eps)
	}
}
