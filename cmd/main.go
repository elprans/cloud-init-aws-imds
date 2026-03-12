package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/session"
)

const (
	minRefreshInterval = 5 * time.Minute
)

var serverStartTime = time.Now().UTC()

type IMDSCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	Code            string `json:"Code"`
	Expiration      string `json:"Expiration"`
	LastUpdated     string `json:"LastUpdated"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Type            string `json:"Type"`
}

// InstanceDataSource reads parsed instance metadata.
type InstanceDataSource interface {
	GetInstanceData() (map[string]interface{}, error)
}

type fileInstanceData struct {
	path string
}

func (f *fileInstanceData) GetInstanceData() (map[string]interface{}, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		klog.Errorf("%s\n", err.Error())
		return nil, err
	}
	jsonMap := make(map[string]interface{})
	if err := json.Unmarshal(data, &jsonMap); err != nil {
		return nil, err
	}
	return jsonMap, nil
}

var dataSource InstanceDataSource

var iamRoleArn string
var iamCredentials *credentials.Credentials
var imdsCredentials *IMDSCredentials

func main() {
	fs := flag.NewFlagSet("nocloud-imds", flag.ExitOnError)
	options := GetOptions(fs)

	dataSource = &fileInstanceData{
		path: "/run/cloud-init/instance-data.json",
	}

	var err error

	iamCredentials, imdsCredentials, iamRoleArn, err = getIAMCredentials()
	if err != nil {
		klog.Fatalf(
			"could not fetch IAM credentials from metadata: %s",
			err,
		)
	}

	if iamCredentials != nil && iamRoleArn != "" {
		config := getAWSConfig(iamCredentials)
		go credRefreshLoop(config)
	}

	http.HandleFunc(
		"/latest/api/token",
		tokenHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/ami-id",
		amiIDHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/instance-id",
		instanceIDHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/instance-type",
		instanceTypeHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/local-hostname",
		localHostnameHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/public-hostname",
		localHostnameHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/local-ipv4",
		func(w http.ResponseWriter, r *http.Request) {
			localIPv4Handler(w, r, options.NetIface)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/public-ipv4",
		func(w http.ResponseWriter, r *http.Request) {
			localIPv4Handler(w, r, options.NetIface)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/hostname",
		localHostnameHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/mac",
		func(w http.ResponseWriter, r *http.Request) {
			macHandler(w, r, options.NetIface)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/network/interfaces/macs",
		func(w http.ResponseWriter, r *http.Request) {
			macsHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/block-device-mapping",
		func(w http.ResponseWriter, r *http.Request) {
			blockDeviceMappingListHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/block-device-mapping/",
		func(w http.ResponseWriter, r *http.Request) {
			blockDeviceMappingHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/iam/info",
		func(w http.ResponseWriter, r *http.Request) {
			iamInfoHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/iam/security-credentials",
		func(w http.ResponseWriter, r *http.Request) {
			iamSecurityCredentialsListHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/iam/security-credentials/",
		func(w http.ResponseWriter, r *http.Request) {
			iamSecurityCredentialsHandler(w, r)
		},
	)

	http.HandleFunc(
		"/latest/meta-data/placement/availability-zone",
		placementAvailabilityZoneHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/tags/instance/",
		tagsInstanceHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/tags/instance",
		tagsInstanceHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/autoscaling/target-lifecycle-state",
		autoscalingLifecycleStateHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/services/domain",
		servicesDomainHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/services/endpoints",
		servicesEndpointsHandler,
	)

	http.HandleFunc(
		"/latest/dynamic/instance-identity/document",
		func(w http.ResponseWriter, r *http.Request) {
			instanceIdentityHandler(w, r, options)
		},
	)

	klog.Fatalln(http.ListenAndServe(
		fmt.Sprintf("%s:%s", options.BindTo, options.Port),
		logRequest(http.DefaultServeMux),
	))
}

func getAWSConfig(creds *credentials.Credentials) *aws.Config {
	fields, err := getV1StandardMetadata()
	if err != nil {
		klog.Fatalf("cannot load metadata: %s", err)
	}
	region, err := getScalarFieldValue(fields, "region", "")
	if err != nil {
		klog.Fatalf("region not in metadata: %s", err)
	}

	klog.Infof("AWS region: %s", region)
	config := aws.NewConfig().WithRegion(region).WithCredentials(creds)
	endpointData, err := getEndpoints()
	if err != nil {
		klog.Fatalf("could not parse AWS endpoints in metadata: %s", err)
	}

	if len(endpointData) != 0 {
		endpointResolver := func(
			service,
			region string,
			optFns ...func(*endpoints.Options),
		) (endpoints.ResolvedEndpoint, error) {
			if url, ok := endpointData[service]; ok {
				klog.Infof("AWS endpoint for %s: %s", service, url)
				return endpoints.ResolvedEndpoint{URL: url}, nil
			}
			return endpoints.DefaultResolver().EndpointFor(
				service, region, optFns...)
		}
		config = config.WithEndpointResolver(
			endpoints.ResolverFunc(endpointResolver))
	}

	return config
}

func credRefreshLoop(config *aws.Config) {
	for {
		sess, err := session.NewSession(config)
		if err != nil {
			klog.Fatalf("could not create AWS session: %v", err)
			continue
		}

		credentials := stscreds.NewCredentials(sess, iamRoleArn)
		creds, err := credentials.Get()
		if err != nil {
			klog.Errorf("could not refresh credentials: %v", err)
			continue
		}
		sess.Config.Credentials = credentials
		iamCredentials = credentials
		imdsCredentials.AccessKeyID = creds.AccessKeyID
		imdsCredentials.SecretAccessKey = creds.SecretAccessKey
		imdsCredentials.Token = creds.SessionToken
		expiresAt, err := iamCredentials.ExpiresAt()
		if err != nil {
			klog.Errorf("could not obtain credentials expiry: %v", err)
			continue
		}
		imdsCredentials.Expiration = expiresAt.Format(time.RFC3339)
		imdsCredentials.LastUpdated = time.Now().UTC().Format(time.RFC3339)

		config = config.WithCredentials(credentials)
		nextRefresh := getCredRefreshInterval(config)

		if credentials.IsExpired() {
			klog.Warning(
				"credentials refreshed successfully, but are still expired")
		} else {
			klog.Infof("credentials refreshed successfully, next refresh in %s",
				nextRefresh.String())
		}

		time.Sleep(nextRefresh)
	}
}

func getCredRefreshInterval(config *aws.Config) time.Duration {
	expiresAt, err := config.Credentials.ExpiresAt()
	if err != nil {
		return minRefreshInterval
	}
	remainingDuration := expiresAt.Sub(time.Now().UTC())
	refreshInterval := remainingDuration / 2
	if refreshInterval < minRefreshInterval {
		refreshInterval = minRefreshInterval
	}
	return refreshInterval
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		klog.V(5).Infof("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		w.Header().Set("Server", "EC2ws")
		w.Header().Set("Content-Type", "text/plain")
		// Token endpoint has its own method check (PUT required).
		// All other endpoints only accept GET.
		if r.URL.Path != "/latest/api/token" && r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func getInstanceData() (map[string]interface{}, error) {
	return dataSource.GetInstanceData()
}

func getV1StandardMetadata() (map[string]interface{}, error) {
	idata, err := getInstanceData()
	if err != nil {
		return nil, err
	}
	fields, found := idata["v1"]
	if !found {
		klog.Errorf("v1 is missing or malformed\n")
		return nil, errors.New("v1 metadata is missing or malformed")
	}
	return fields.(map[string]interface{}), nil
}

func getDSMetadata() (map[string]interface{}, error) {
	idata, err := getInstanceData()
	if err != nil {
		return nil, err
	}
	ds, found := idata["ds"]
	if !found {
		klog.Errorf("ds metadata is missing or malformed\n")
		return nil, errors.New("ds metadata is missing or malformed")
	}
	fields, found := ds.(map[string]interface{})["meta_data"]
	if !found {
		klog.Errorf("ds metadata is missing or malformed\n")
		return nil, errors.New("ds metadata is missing or malformed")
	}
	return fields.(map[string]interface{}), nil
}

func getScalarFieldValue(
	fields map[string]interface{},
	name string,
	deflt string,
) (string, error) {
	val, found := fields[name]
	if !found {
		name = strings.ReplaceAll(name, "-", "_")
		val, found = fields[name]
		if !found {
			name = strings.ReplaceAll(name, "_", "-")
			val, found = fields[name]
			if !found {
				if deflt != "" {
					return deflt, nil
				}
				klog.Errorf("'%s' metadata value is missing\n", name)
				return "", fmt.Errorf("%s is missing in metadata", name)
			}
		}
	}

	switch v := val.(type) {
	case string:
		return v, nil
	default:
		klog.Errorf("'%s' metadata value is not a string\n", name)
		return "", fmt.Errorf("%s value is not a string", name)
	}
}

func getMapFieldValue(
	fields map[string]interface{},
	name string,
	deflt map[string]interface{},
) (map[string]interface{}, error) {
	val, found := fields[name]
	if !found {
		name = strings.ReplaceAll(name, "-", "_")
		val, found = fields[name]
		if !found {
			name = strings.ReplaceAll(name, "_", "-")
			val, found = fields[name]
			if !found {
				if deflt != nil {
					return deflt, nil
				}
				klog.Errorf("'%s' metadata value is missing\n", name)
				return nil, fmt.Errorf("%s is missing in metadata", name)
			}
		}
	}

	if val == nil {
		return nil, nil
	}

	switch v := val.(type) {
	case map[string]interface{}:
		return v, nil
	default:
		klog.Errorf("'%s' metadata value is not a map\n", name)
		return nil, fmt.Errorf("%s value is not a map", name)
	}
}

func getV1FieldValue(
	name string,
	deflt string,
) (string, error) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		return "", err
	}
	return getScalarFieldValue(fields, name, deflt)
}

func getDSFieldValue(
	name string,
	deflt string,
) (string, error) {
	fields, err := getDSMetadata()
	if err != nil {
		return "", err
	}
	return getScalarFieldValue(fields, name, deflt)
}

func formatFields(
	format string,
	fields map[string]interface{},
	names ...string,
) (string, error) {
	vals := make([]interface{}, len(names))

	for i, name := range names {
		val, err := getScalarFieldValue(fields, name, "")
		if err != nil {
			return "", err
		}
		vals[i] = val
	}

	return fmt.Sprintf(format, vals...), nil
}

func formatV1Fields(
	format string,
	names ...string,
) (string, error) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		return "", err
	}

	return formatFields(format, fields, names...)
}

func formatDSFields(
	format string,
	names ...string,
) (string, error) {
	fields, err := getDSMetadata()
	if err != nil {
		return "", err
	}

	return formatFields(format, fields, names...)
}

func getIAMCredentials() (*credentials.Credentials, *IMDSCredentials, string, error) {
	fields, err := getDSMetadata()
	if err != nil {
		return nil, nil, "", err
	}
	iam, err := getMapFieldValue(fields, "iam", make(map[string]interface{}))
	if err != nil {
		return nil, nil, "", err
	}
	if len(iam) == 0 {
		return nil, nil, "", nil
	}
	role, _ := getScalarFieldValue(iam, "role-arn", "")
	creds, err := getMapFieldValue(
		iam, "credentials", make(map[string]interface{}))
	if err != nil {
		return nil, nil, "", err
	}
	if len(creds) == 0 {
		return nil, nil, "", nil
	}
	klog.Info("loaded IAM credentials from metadata")
	iamCreds := credentials.NewStaticCredentials(
		creds["AccessKeyId"].(string),
		creds["SecretAccessKey"].(string),
		creds["Token"].(string),
	)
	imdsCreds := &IMDSCredentials{
		AccessKeyID:     creds["AccessKeyId"].(string),
		SecretAccessKey: creds["SecretAccessKey"].(string),
		Token:           creds["Token"].(string),
		Code:            creds["Code"].(string),
		Expiration:      creds["Expiration"].(string),
		LastUpdated:     creds["LastUpdated"].(string),
		Type:            creds["Type"].(string),
	}
	return iamCreds, imdsCreds, role, nil
}

func tokenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Forwarded-For") != "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	ttl := r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds")
	if ttl == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if _, err := strconv.Atoi(ttl); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	token := base64.URLEncoding.EncodeToString([]byte("dummytoken"))
	fmt.Fprintf(w, "%s", token)
}

func amiIDHandler(w http.ResponseWriter, _ *http.Request) {
	val, err := formatV1Fields("%s-%s", "distro", "distro_release")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", val)
}

func localHostnameHandler(w http.ResponseWriter, _ *http.Request) {
	val, err := formatDSFields("%s", "local_hostname")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", val)
}

func getLocalIPv4Address(iface string) (string, error) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		return "", err
	}

	addrs, err := iff.Addrs()
	if err != nil {
		return "", err
	}

	var ipString string

	for _, addr := range addrs {
		switch v := addr.(type) {
		case *net.IPAddr:
		case *net.IPNet:
			if v.IP.To4() != nil {
				ipString = v.IP.String()
				break
			}
		}
	}

	if ipString == "" {
		msg := fmt.Sprintf("cannot determine address of %s", iface)
		klog.Errorf("%s\n", msg)
		return "", errors.New(msg)
	}

	return ipString, nil
}

func localIPv4Handler(w http.ResponseWriter, _ *http.Request, iface string) {
	ipString, err := getLocalIPv4Address(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", ipString)
}

func instanceIDHandler(w http.ResponseWriter, r *http.Request) {
	val, err := formatV1Fields("%s", "instance_id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", val)
}

func instanceTypeHandler(w http.ResponseWriter, r *http.Request) {
	instType, err := getDSFieldValue("instance_type", "t2.micro")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", instType)
}

// iamInfoResponse matches the JSON structure returned by real AWS IMDS at
// /latest/meta-data/iam/info.
type iamInfoResponse struct {
	Code               string `json:"Code"`
	LastUpdated        string `json:"LastUpdated"`
	InstanceProfileArn string `json:"InstanceProfileArn"`
	InstanceProfileID  string `json:"InstanceProfileId"`
}

func iamInfoHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	iam, err := getMapFieldValue(fields, "iam", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(iam) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	profile, err := getMapFieldValue(
		iam, "instance-profile", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(profile) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	arn, _ := getScalarFieldValue(profile, "arn", "")
	id, _ := getScalarFieldValue(profile, "id", "")

	info := iamInfoResponse{
		Code:               "Success",
		LastUpdated:        time.Now().UTC().Format(time.RFC3339),
		InstanceProfileArn: arn,
		InstanceProfileID:  id,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", data)
}

func iamSecurityCredentialsListHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	iam, err := getMapFieldValue(fields, "iam", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(iam) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	roleName, err := getScalarFieldValue(iam, "role-name", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if roleName == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, "%s", roleName)
}

func iamSecurityCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/latest/meta-data/iam/security-credentials/" {
		iamSecurityCredentialsListHandler(w, r)
		return
	}

	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	iam, err := getMapFieldValue(fields, "iam", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(iam) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	roleInURL := path.Base(r.URL.Path)
	roleInMetadata, err := getScalarFieldValue(iam, "role-name", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.Compare(roleInURL, roleInMetadata) != 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if iamCredentials == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, err := json.MarshalIndent(imdsCredentials, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", data)
}

func macHandler(w http.ResponseWriter, r *http.Request, iface string) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", iff.HardwareAddr.String())
}

func macsHandler(w http.ResponseWriter, r *http.Request) {
	iff, err := net.Interfaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	first := true
	for _, iface := range iff {
		// Skip loopback (empty MAC) and virtual interfaces.
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "docker") ||
			strings.HasPrefix(iface.Name, "veth") {
			continue
		}
		if !first {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "%s/", iface.HardwareAddr.String())
		first = false
	}
}

func blockDeviceMappingListHandler(w http.ResponseWriter, r *http.Request) {
	devices, err := getBlockDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	i := 0
	for dev := range devices {
		if i > 0 {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "%s", dev)
		i++
	}
}

func blockDeviceMappingHandler(w http.ResponseWriter, r *http.Request) {
	devices, err := getBlockDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	device := path.Base(r.URL.Path)
	path, ok := devices[device]
	if !ok {
		http.Error(w, "No such device", http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, "%s", path)
}

func placementAvailabilityZoneHandler(w http.ResponseWriter, r *http.Request) {
	az, err := getV1FieldValue("availability_zone", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", az)
}

func servicesDomainHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	services, err := getMapFieldValue(fields, "services", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if services == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	dom, err := getScalarFieldValue(services, "domain", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if dom == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, "%s", dom)
}

func getEndpoints() (map[string]string, error) {
	fields, err := getDSMetadata()
	if err != nil {
		return nil, err
	}

	services, err := getMapFieldValue(fields, "services", nil)
	if err != nil {
		return nil, err
	}
	if services == nil {
		return nil, nil
	}

	endpoints, err := getMapFieldValue(services, "endpoints", nil)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(endpoints))
	for k, v := range endpoints {
		result[k] = v.(string)
	}

	return result, nil
}

func servicesEndpointsHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	services, err := getMapFieldValue(fields, "services", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if services == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	endpoints, err := getMapFieldValue(services, "endpoints", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if endpoints == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	data, err := json.Marshal(endpoints)
	if err != nil {
		http.Error(
			w,
			fmt.Errorf("cannot marshal json: %w", err).Error(),
			http.StatusInternalServerError,
		)
		return
	}

	w.Write(data)
}

// instanceIdentityDocument matches the JSON structure returned by real AWS IMDS.
// Field order is deterministic because encoding/json marshals struct fields in
// declaration order.
type instanceIdentityDocument struct {
	DevpayProductCodes      *[]string `json:"devpayProductCodes"`
	MarketplaceProductCodes *[]string `json:"marketplaceProductCodes"`
	AvailabilityZone        string    `json:"availabilityZone"`
	PrivateIP               string    `json:"privateIp"`
	Version                 string    `json:"version"`
	InstanceID              string    `json:"instanceId"`
	BillingProducts         *[]string `json:"billingProducts"`
	InstanceType            string    `json:"instanceType"`
	AccountID               string    `json:"accountId"`
	ImageID                 string    `json:"imageId"`
	PendingTime             string    `json:"pendingTime"`
	Architecture            string    `json:"architecture"`
	KernelID                *string   `json:"kernelId"`
	RamdiskID               *string   `json:"ramdiskId"`
	Region                  string    `json:"region"`
}

func instanceIdentityHandler(
	w http.ResponseWriter,
	r *http.Request,
	options *Options,
) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dsfields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	az, err := getScalarFieldValue(fields, "availability_zone", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	instID, err := getScalarFieldValue(fields, "instance_id", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	instType, err := getScalarFieldValue(dsfields, "instance_type", "t2.micro")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	imageID, err := formatV1Fields("%s %s", "distro", "distro_release")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	machine, err := getScalarFieldValue(fields, "machine", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	region, err := getScalarFieldValue(fields, "region", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ipString, err := getLocalIPv4Address(options.NetIface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	doc := instanceIdentityDocument{
		DevpayProductCodes:      nil,
		MarketplaceProductCodes: nil,
		AvailabilityZone:        az,
		PrivateIP:               ipString,
		Version:                 "2017-09-30",
		InstanceID:              instID,
		BillingProducts:         nil,
		InstanceType:            instType,
		AccountID:               options.AccountID,
		ImageID:                 imageID,
		PendingTime:             serverStartTime.Format(time.RFC3339),
		Architecture:            machine,
		KernelID:                nil,
		RamdiskID:               nil,
		Region:                  region,
	}

	jsonData, err := json.Marshal(doc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Write(jsonData)
}

func tagsInstanceHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tags, err := getMapFieldValue(fields, "tags", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(tags) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Strip trailing slash and prefix to get the tag key
	reqPath := strings.TrimSuffix(r.URL.Path, "/")
	prefix := "/latest/meta-data/tags/instance"

	if reqPath == prefix {
		// List all tag keys
		i := 0
		for key := range tags {
			if i > 0 {
				fmt.Fprintf(w, "\n")
			}
			fmt.Fprintf(w, "%s", key)
			i++
		}
		return
	}

	// Get specific tag value
	tagKey := strings.TrimPrefix(reqPath, prefix+"/")
	if tagKey == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	val, ok := tags[tagKey]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	fmt.Fprintf(w, "%s", val.(string))
}

func autoscalingLifecycleStateHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	autoscaling, err := getMapFieldValue(
		fields, "autoscaling", make(map[string]interface{}))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(autoscaling) == 0 {
		fmt.Fprintf(w, "InService")
		return
	}

	state, err := getScalarFieldValue(
		autoscaling, "target_lifecycle_state", "InService")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "%s", state)
}

func getBlockDevices() (map[string]string, error) {
	disks := "/dev/disk/by-label"
	dir, err := os.ReadDir(disks)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)

	for _, f := range dir {
		name := f.Name()
		if strings.Compare(name, "UEFI") == 0 ||
			strings.Compare(name, "cidata") == 0 {
			continue
		}

		node, err := filepath.EvalSymlinks(path.Join(disks, name))
		if err != nil {
			continue
		}

		result[name] = node
	}

	return result, nil
}
