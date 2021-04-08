# EC2-compatible Instance Metadata Service for Cloud-init

This is a simple Web-service designed to emulate AWS IMDS v2 to make non-EC2
VMs pretend like they are.  The only requirement is that the VM runs cloud-init
(or otherwise populates `/run/cloud-init/instance-data.json` with appropriate
data).

For better compatibility VM instance metadata should contain the following
keys:

- `region`
- `availability-zone`
- `instance-id`
