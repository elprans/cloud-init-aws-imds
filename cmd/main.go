package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"

	"k8s.io/klog/v2"
)

func main() {
	fs := flag.NewFlagSet("nocloud-imds", flag.ExitOnError)
	options := GetOptions(fs)

	http.HandleFunc(
		"/latest/meta-data/instance-id",
		instanceIdHandler,
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

func instanceIdHandler(w http.ResponseWriter, r *http.Request) {
	idata, err := getInstanceData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	} else {
		fields := idata["v1"].(map[string]interface{})
		fmt.Fprintf(w, "%s", fields["instance-id"])
	}
}

func instanceIdentityHandler(w http.ResponseWriter, r *http.Request) {
	idata, err := getInstanceData()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	fields := idata["v1"].(map[string]interface{})

	result := make(map[string]interface{})
	result["devpayProductCodes"] = make([]string, 0)
	result["marketplaceProductCodes"] = make([]string, 0)
	result["availabilityZone"] = fields["availability-zone"]
	result["privateIp"] = nil
	result["version"] = "2017-09-30"
	result["instanceId"] = fields["instance-id"]
	result["billingProducts"] = nil
	result["instanceType"] = "t2.micro"
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
