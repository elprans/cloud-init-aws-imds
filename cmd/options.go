/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"k8s.io/klog/v2"
)

// Options is the combined set of options for all operating modes.
type Options struct {
	BindTo   string
	Port     string
	NetIface string
}

func GetOptions(fs *flag.FlagSet) *Options {
	var (
		version = fs.Bool("version", false, "Print the version and exit.")
		bindTo  = fs.String("bind-to", "169.254.169.254", "Address to bind to.")
		port    = fs.String("port", "80", "Port to bind to.")
		iface   = fs.String("net-iface", "", "Network interface used for traffic.")

		args = os.Args[1:]
	)

	klog.InitFlags(fs)

	if err := fs.Parse(args); err != nil {
		panic(err)
	}

	if *version {
		fmt.Println("1.0")
		os.Exit(0)
	}

	if *iface == "" {
		ifaces, err := net.Interfaces()
		if err != nil {
			panic(err)
		}

		for _, i := range ifaces {
			if !strings.HasPrefix(i.Name, "lo") &&
				!strings.HasPrefix(i.Name, "docker") {
				*iface = i.Name
				break
			}
		}
	}

	return &Options{
		BindTo:   *bindTo,
		Port:     *port,
		NetIface: *iface,
	}
}
