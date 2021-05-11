package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"k8s.io/klog/v2"
)

func main() {
	fs := flag.NewFlagSet("nocloud-imds", flag.ExitOnError)
	options := GetOptions(fs)

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
		"/latest/meta-data/placement/availability-zone",
		placementAvailabilityZoneHandler,
	)

	http.HandleFunc(
		"/latest/dynamic/instance-identity/document",
		instanceIdentityHandler,
	)

	klog.Fatalln(http.ListenAndServe(
		fmt.Sprintf("%s:%s", options.BindTo, options.Port),
		logRequest(http.DefaultServeMux),
	))
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
			return nil, errors.New("ds metadata is missing or malformed")
		}
		fields, found := ds.(map[string]interface{})["meta_data"]
		if !found {
			return nil, errors.New("ds metadata is missing or malformed")
		}
		return fields.(map[string]interface{}), nil
	}
}

func amiIdHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s-%s", fields["distro"], fields["distro_release"])
	}
}

func localHostnameHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", fields["local_hostname"])
	}
}

func localIPv4Handler(w http.ResponseWriter, r *http.Request, iface string) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	addrs, err := iff.Addrs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(
			w,
			fmt.Sprintf("cannot determine address of %s", iface),
			http.StatusInternalServerError)
	}

	fmt.Fprintf(w, "%s", ipString)
}

func instanceIdHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", fields["instance_id"])
	}
}

func instanceTypeHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		instType := fields["instance_type"]
		if instType == nil {
			instType = "t2.micro"
		}
		fmt.Fprintf(w, "%s", instType)
	}
}

func macHandler(w http.ResponseWriter, r *http.Request, iface string) {
	iff, err := net.InterfaceByName(iface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	fmt.Fprintf(w, "%s", iff.HardwareAddr.String())
}

func placementAvailabilityZoneHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fmt.Fprintf(w, "%s", fields["availability_zone"])
	}
}

func instanceIdentityHandler(w http.ResponseWriter, r *http.Request) {
	fields, err := getV1StandardMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	dsfields, err := getDSMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	result := make(map[string]interface{})
	result["devpayProductCodes"] = make([]string, 0)
	result["marketplaceProductCodes"] = make([]string, 0)
	result["availabilityZone"] = fields["availability_zone"]
	result["privateIp"] = nil
	result["version"] = "2017-09-30"
	result["instanceId"] = fields["instance_id"]
	result["billingProducts"] = nil
	if dsfields["instance_type"] != nil {
		result["instanceType"] = dsfields["instance_type"]
	} else {
		result["instanceType"] = "t2.micro"
	}
	result["accountId"] = "invalid"
	result["imageId"] = fmt.Sprintf(
		"%s %s", fields["distro"], fields["distro_release"])
	result["pendingTime"] = nil
	result["architecture"] = fields["machine"]
	result["kernelId"] = nil
	result["ramdiskId"] = nil
	result["region"] = fields["region"]

	jsonData, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	w.Write(jsonData)
}
