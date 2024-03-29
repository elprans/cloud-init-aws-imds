package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"path"
	"path/filepath"
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

type IMDSCredentials struct {
	AccessKeyId     string
	Code            string
	Expiration      string
	LastUpdated     string
	SecretAccessKey string
	Token           string
	Type            string
}

var iamRoleArn string
var iamCredentials *credentials.Credentials
var imdsCredentials *IMDSCredentials

func main() {
	fs := flag.NewFlagSet("nocloud-imds", flag.ExitOnError)
	options := GetOptions(fs)

	var err error

	iamCredentials, imdsCredentials, iamRoleArn, err = getIAMCredentials()
	if err != nil {
		klog.Fatalf(
			"could not fetch IAM credentials from metadata: %s",
			err,
		)
	}

	if iamCredentials != nil {
		config := getAWSConfig(iamCredentials)
		go credRefreshLoop(config)
	}

	http.HandleFunc(
		"/latest/api/token",
		tokenHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/ami-id",
		amiIdHandler,
	)

	http.HandleFunc(
		"/latest/meta-data/instance-id",
		instanceIdHandler,
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
			} else {
				return endpoints.DefaultResolver().EndpointFor(
					service, region, optFns...)
			}
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
		imdsCredentials.AccessKeyId = creds.AccessKeyID
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
	} else {
		remainingDuration := expiresAt.Sub(time.Now().UTC())
		refreshInterval := remainingDuration / 2
		if refreshInterval < minRefreshInterval {
			refreshInterval = minRefreshInterval
		}

		return refreshInterval
	}
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		klog.V(5).Infof("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

func getInstanceData() (map[string]interface{}, error) {
	data, err := ioutil.ReadFile("/run/cloud-init/instance-data.json")
	if err != nil {
		klog.Errorf("%s\n", err.Error())
		return nil, err
	}

	jsonMap := make(map[string]interface{})
	err = json.Unmarshal(data, &jsonMap)
	if err != nil {
		return nil, err
	}

	return jsonMap, nil
}

func getV1StandardMetadata() (map[string]interface{}, error) {
	idata, err := getInstanceData()
	if err != nil {
		return nil, err
	} else {
		fields, found := idata["v1"]
		if !found {
			klog.Errorf("v1 is missing or malformed\n")
			return nil, errors.New("v1 metadata is missing or malformed")
		}
		return fields.(map[string]interface{}), nil
	}
}

func getDSMetadata() (map[string]interface{}, error) {
	idata, err := getInstanceData()
	if err != nil {
		return nil, err
	} else {
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
				} else {
					klog.Errorf("'%s' metadata value is missing\n", name)
					return "", errors.New(
						fmt.Sprintf("%s is missing in metadata", name))
				}
			}
		}
	}

	switch v := val.(type) {
	case string:
		return v, nil
	default:
		klog.Errorf("'%s' metadata value is not a string\n", name)
		return "", errors.New(
			fmt.Sprintf("%s value is not a string", name))
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
				} else {
					klog.Errorf("'%s' metadata value is missing\n", name)
					return nil, errors.New(
						fmt.Sprintf("%s is missing in metadata", name))
				}
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
	role, err := getScalarFieldValue(iam, "role-arn", "")
	if err != nil {
		return nil, nil, "", err
	}
	creds, err := getMapFieldValue(
		iam, "credentials", make(map[string]interface{}))
	if err != nil {
		return nil, nil, "", err
	}
	if len(creds) == 0 {
		return nil, nil, "", nil
	} else {
		klog.Info("loaded IAM credentials from metadata")
		iamCreds := credentials.NewStaticCredentials(
			creds["AccessKeyId"].(string),
			creds["SecretAccessKey"].(string),
			creds["Token"].(string),
		)
		imdsCreds := &IMDSCredentials{
			AccessKeyId:     creds["AccessKeyId"].(string),
			SecretAccessKey: creds["SecretAccessKey"].(string),
			Token:           creds["Token"].(string),
			Code:            creds["Code"].(string),
			Expiration:      creds["Expiration"].(string),
			LastUpdated:     creds["LastUpdated"].(string),
			Type:            creds["Type"].(string),
		}
		return iamCreds, imdsCreds, role, nil
	}
}

func tokenHandler(w http.ResponseWriter, r *http.Request) {
	token := base64.URLEncoding.EncodeToString([]byte("dummytoken"))
	fmt.Fprintf(w, "%s", token)
}

func amiIdHandler(w http.ResponseWriter, r *http.Request) {
	val, err := formatV1Fields("%s-%s", "distro", "distro_release")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", val)
	}
}

func localHostnameHandler(w http.ResponseWriter, r *http.Request) {
	val, err := formatDSFields("%s", "local_hostname")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", val)
	}
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

func localIPv4Handler(w http.ResponseWriter, r *http.Request, iface string) {
	ipString, err := getLocalIPv4Address(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fmt.Fprintf(w, "%s", ipString)
}

func instanceIdHandler(w http.ResponseWriter, r *http.Request) {
	val, err := formatV1Fields("%s", "instance_id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", val)
	}
}

func instanceTypeHandler(w http.ResponseWriter, r *http.Request) {
	instType, err := getDSFieldValue("instance_type", "t2.micro")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", instType)
	}
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
	} else {
		data, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			fmt.Fprintf(w, "%s", data)
		}
	}
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
	} else {
		fmt.Fprintf(w, "%s", roleName)
	}
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

	roleInUrl := path.Base(r.URL.Path)
	roleInMetadata, err := getScalarFieldValue(iam, "role-name", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.Compare(roleInUrl, roleInMetadata) != 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if iamCredentials == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else {
		data, err := json.MarshalIndent(imdsCredentials, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			fmt.Fprintf(w, "%s", data)
		}
	}
}

func macHandler(w http.ResponseWriter, r *http.Request, iface string) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fmt.Fprintf(w, "%s", iff.HardwareAddr.String())
}

func macsHandler(w http.ResponseWriter, r *http.Request) {
	iff, err := net.Interfaces()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	for i, iface := range iff {
		if i > 0 {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "%s/", iface.HardwareAddr.String())
	}
}

func blockDeviceMappingListHandler(w http.ResponseWriter, r *http.Request) {
	devices, err := getBlockDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	i := 0
	for dev := range devices {
		if i > 0 {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "%s", dev)
		i += 1
	}
}

func blockDeviceMappingHandler(w http.ResponseWriter, r *http.Request) {
	devices, err := getBlockDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	device := path.Base(r.URL.Path)
	path, ok := devices[device]
	if !ok {
		http.Error(w, "No such device", http.StatusNotFound)
	}
	fmt.Fprintf(w, "%s", path)
}

func placementAvailabilityZoneHandler(w http.ResponseWriter, r *http.Request) {
	az, err := getV1FieldValue("availability_zone", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", az)
	}
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
	} else {
		fmt.Fprintf(w, "%s", dom)
	}
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

func instanceIdentityHandler(
	w http.ResponseWriter,
	r *http.Request,
	options *Options,
) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	dsfields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	az, err := getScalarFieldValue(fields, "availability_zone", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	instId, err := getScalarFieldValue(fields, "instance_id", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	instType, err := getScalarFieldValue(dsfields, "instance_type", "t2.micro")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	imageId, err := formatV1Fields("%s %s", "distro", "distro_release")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	machine, err := getScalarFieldValue(fields, "machine", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	region, err := getScalarFieldValue(fields, "region", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	ipString, err := getLocalIPv4Address(options.NetIface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	result := make(map[string]interface{})
	result["devpayProductCodes"] = make([]string, 0)
	result["marketplaceProductCodes"] = make([]string, 0)
	result["availabilityZone"] = az
	result["privateIp"] = ipString
	result["version"] = "2017-09-30"
	result["instanceId"] = instId
	result["instanceType"] = instType
	result["billingProducts"] = nil
	result["accountId"] = "invalid"
	result["imageId"] = imageId
	result["pendingTime"] = nil
	result["architecture"] = machine
	result["kernelId"] = nil
	result["ramdiskId"] = nil
	result["region"] = region

	jsonData, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	w.Write(jsonData)
}

func getBlockDevices() (map[string]string, error) {
	disks := "/dev/disk/by-label"
	dir, err := ioutil.ReadDir(disks)
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
