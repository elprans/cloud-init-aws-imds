package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

func newSDKClient(t *testing.T, serverURL string) *imds.Client {
	t.Helper()
	return imds.New(imds.Options{
		Endpoint: serverURL,
	})
}

// newSDKTestServer creates an httptest.Server with full test data,
// block devices, and IAM credentials populated.
func newSDKTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	data := baseTestData()
	// Add endpoints for services/endpoints test.
	ds := data["ds"].(map[string]interface{})
	md := ds["meta_data"].(map[string]interface{})
	md["services"] = map[string]interface{}{
		"domain": "amazonaws.com",
		"endpoints": map[string]interface{}{
			"sts": "https://sts.us-west-2.amazonaws.com",
		},
	}

	s := newTestServerWithIAM(t, data)
	s.blockDevices = &mockBlockDeviceSource{
		devices: map[string]string{
			"root": "/dev/sda1",
			"data": "/dev/sdb",
		},
	}

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func TestSDKGetMetadata(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	tests := []struct {
		name  string
		path  string
		exact string
	}{
		{"ami-id", "ami-id", "debian-bookworm"},
		{"instance-id", "instance-id", "i-test-1234"},
		{"instance-type", "instance-type", "m7g.metal-48xl"},
		{"local-hostname", "local-hostname", "test-host"},
		{"public-hostname", "public-hostname", "test-host"},
		{"hostname", "hostname", "test-host"},
		{"local-ipv4", "local-ipv4", "10.0.0.42"},
		{"public-ipv4", "public-ipv4", "10.0.0.42"},
		{"mac", "mac", "aa:bb:cc:dd:ee:ff"},
		{"placement/availability-zone", "placement/availability-zone", "us-west-2a"},
		{"services/domain", "services/domain", "amazonaws.com"},
		{"autoscaling/target-lifecycle-state", "autoscaling/target-lifecycle-state", "InService"},
		{"iam/security-credentials", "iam/security-credentials", "test-role"},
		{"tags/instance/Name", "tags/instance/Name", "test-instance"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
				Path: tc.path,
			})
			if err != nil {
				t.Fatalf("GetMetadata(%q) failed: %v", tc.path, err)
			}
			body, _ := io.ReadAll(out.Content)
			if string(body) != tc.exact {
				t.Errorf("GetMetadata(%q) = %q, want %q", tc.path, body, tc.exact)
			}
		})
	}
}

func TestSDKGetMetadataMacs(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "network/interfaces/macs",
	})
	if err != nil {
		t.Fatalf("GetMetadata(macs) failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)
	// Each MAC line ends with "/".
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "/") {
			t.Errorf("expected MAC line to end with '/', got %q", line)
		}
	}
}

func TestSDKGetMetadataBlockDevices(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	// List block devices.
	out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "block-device-mapping",
	})
	if err != nil {
		t.Fatalf("GetMetadata(block-device-mapping) failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)
	if !strings.Contains(string(body), "root") {
		t.Errorf("expected 'root' in block device list, got %q", body)
	}

	// Get specific device.
	out, err = client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "block-device-mapping/root",
	})
	if err != nil {
		t.Fatalf("GetMetadata(block-device-mapping/root) failed: %v", err)
	}
	body, _ = io.ReadAll(out.Content)
	if string(body) != "/dev/sda1" {
		t.Errorf("expected '/dev/sda1', got %q", body)
	}
}

func TestSDKGetMetadataTagsList(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "tags/instance",
	})
	if err != nil {
		t.Fatalf("GetMetadata(tags/instance) failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)
	if !strings.Contains(string(body), "Name") {
		t.Errorf("expected 'Name' in tag list, got %q", body)
	}
}

func TestSDKGetMetadataIAMCredentials(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "iam/security-credentials/test-role",
	})
	if err != nil {
		t.Fatalf("GetMetadata(iam/security-credentials/test-role) failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)

	var creds IMDSCredentials
	if err := json.Unmarshal(body, &creds); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if creds.AccessKeyID != "AKIATEST" {
		t.Errorf("expected AccessKeyId=AKIATEST, got %q", creds.AccessKeyID)
	}
	if creds.Code != "Success" {
		t.Errorf("expected Code=Success, got %q", creds.Code)
	}
}

func TestSDKGetMetadataServicesEndpoints(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "services/endpoints",
	})
	if err != nil {
		t.Fatalf("GetMetadata(services/endpoints) failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if result["sts"] != "https://sts.us-west-2.amazonaws.com" {
		t.Errorf("unexpected sts endpoint: %v", result["sts"])
	}
}

func TestSDKGetIAMInfo(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetIAMInfo(ctx, &imds.GetIAMInfoInput{})
	if err != nil {
		t.Fatalf("GetIAMInfo failed: %v", err)
	}

	if out.Code != "Success" {
		t.Errorf("expected Code=Success, got %q", out.Code)
	}
	if out.InstanceProfileArn != "arn:aws:iam::123456789012:instance-profile/test-profile" {
		t.Errorf("unexpected InstanceProfileArn: %q", out.InstanceProfileArn)
	}
	if out.InstanceProfileID != "AIPA1234567890" {
		t.Errorf("unexpected InstanceProfileId: %q", out.InstanceProfileID)
	}
}

func TestSDKGetInstanceIdentityDocument(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetInstanceIdentityDocument(ctx, &imds.GetInstanceIdentityDocumentInput{})
	if err != nil {
		t.Fatalf("GetInstanceIdentityDocument failed: %v", err)
	}

	if out.Region != "us-west-2" {
		t.Errorf("expected Region=us-west-2, got %q", out.Region)
	}
	if out.AvailabilityZone != "us-west-2a" {
		t.Errorf("expected AZ=us-west-2a, got %q", out.AvailabilityZone)
	}
	if out.InstanceID != "i-test-1234" {
		t.Errorf("expected InstanceID=i-test-1234, got %q", out.InstanceID)
	}
	if out.InstanceType != "m7g.metal-48xl" {
		t.Errorf("expected InstanceType=m7g.metal-48xl, got %q", out.InstanceType)
	}
	if out.AccountID != "123456789012" {
		t.Errorf("expected AccountID=123456789012, got %q", out.AccountID)
	}
	if out.Architecture != "aarch64" {
		t.Errorf("expected Architecture=aarch64, got %q", out.Architecture)
	}
	if out.PrivateIP != "10.0.0.42" {
		t.Errorf("expected PrivateIP=10.0.0.42, got %q", out.PrivateIP)
	}
	if out.PendingTime.IsZero() {
		t.Error("expected non-zero PendingTime")
	}
}

func TestSDKGetRegion(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetRegion(ctx, &imds.GetRegionInput{})
	if err != nil {
		t.Fatalf("GetRegion failed: %v", err)
	}
	if out.Region != "us-west-2" {
		t.Errorf("expected Region=us-west-2, got %q", out.Region)
	}
}

func TestSDKGetDynamicData(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	out, err := client.GetDynamicData(ctx, &imds.GetDynamicDataInput{
		Path: "instance-identity/document",
	})
	if err != nil {
		t.Fatalf("GetDynamicData failed: %v", err)
	}
	body, _ := io.ReadAll(out.Content)
	if !strings.Contains(string(body), "us-west-2") {
		t.Errorf("expected identity doc to contain region, got %q", body)
	}
}

func TestSDKTokenFlow(t *testing.T) {
	_, srv := newSDKTestServer(t)
	client := newSDKClient(t, srv.URL)
	ctx := context.Background()

	// Verify that the SDK can acquire a token and use it for metadata.
	// This exercises the full IMDSv2 flow: PUT token → GET with token header.
	// First call acquires a token, second reuses it.
	for i := 0; i < 2; i++ {
		out, err := client.GetMetadata(ctx, &imds.GetMetadataInput{
			Path: "placement/availability-zone",
		})
		if err != nil {
			t.Fatalf("request %d: GetMetadata failed: %v", i, err)
		}
		body, _ := io.ReadAll(out.Content)
		if string(body) != "us-west-2a" {
			t.Errorf("request %d: expected us-west-2a, got %q", i, body)
		}
	}
}
