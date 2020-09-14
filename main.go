package main

import (
	"flag"
	"os/exec"
	"strings"

	"github.com/coreos/fleet/etcd"
	"github.com/fsouza/go-dockerclient"
	"github.com/golang/glog"
)

// LocalEtcdClient
type LocalEtcdClient struct {
	etcd.Client
}

// Settings
var etcdServers []string
var bridgeInterface string

// Global variables
var instance string
var installPath string

func init() {
	var serversString string
	flag.StringVar(&instance, "i", "", "Instance (required)")
	flag.StringVar(&installPath, "t", "/var/lib/cycore/qemu", "Target path (for qemu chroot)")
	flag.StringVar(&serversString, "etcdServer", "http://127.0.0.1:4001", "etcd servers")
	flag.StringVar(&bridgeInterface, "bridgeInterface", "public", "bridge interface")

	// Parse the servers string
	etcdServers = strings.Split(serversString, ",")
}

func main() {
	flag.Parse()

	if len(instance) < 1 {
		glog.Fatalln("Instance (id) is required")
	}

	// Install qemu docker image to the target path
	err := installQemu()
	if err != nil {
		glog.Fatalln("Failed to install Qemu chroot", err)
	}

	// Get etcd settings for this VM
	ec := LocalEtcdClient{etcd.NewClient(etcdServers)}
	ram := ec.GetValue("/kvm/" + instance + "/ram")
	mac := ec.GetValue("/kvm/" + instance + "/mac")
	rbd := ec.GetValue("/kvm/" + instance + "/rbd")
	spice_port := ec.GetValue("/kvm/" + instance + "/spice_port")

	// Construct execution command
	cmd := exec.Command("/usr/bin/systemd-nspawn", "-D", installPath, "--share-system",
		"--capability=all", "--bind", "/etc/ceph:/etc/ceph",
		"--setenv=BRIDGE_IF"+bridgeInterface,
		"/bin/bash", "/usr/local/bin/entrypoint.sh",
		"-vga", "qxl", "-spice", "port="+spice_port+",addr=127.0.0.1,disable-ticketing",
		"-k", "en-us",
		"-m", ram, "-cpu", "qemu64",
		"-netdev", "bridge,br="+bridgeInterface+"id=net0",
		"-device", "virtio-net,netdev=net0,mac="+mac,
		"-device", "format=rbd,file=rbd:"+rbd+",cache=writeback,if=virtio")
	// FIXME:  Unfinished
}

// getValue is a thin wrapper around etcd.Get which
// simply extracts the end value
func (ec *LocalEtcdClient) GetValue(key string) string {
	res, err := ec.Get(key)
	if err != nil {
		glog.Fatalln("Failed to get value for", key, "from etcd", err)
	}
	if res == "" {
		glog.Fatalln("Key", key, "in etcd is unset")
	}
	return res.Node.Value, nil
}

func installQemu() error {
	dc := docker.NewClient("unix:///var/run/docker.sock")
	auth := docker.AuthConfiguration{}
	source := docker.PullImageOptions{
		Repository: "ulexus",
		Registry:   "qemu",
		Tag:        "latest",
	}
	err := dc.PullImage(source, auth)
	if err != nil {
		glog.Errorln("Failed to pull ulexus/qemu")
		return err
	}

	// Create the configuration for the new image
	config := docker.Config{}
	append(config.Entrypoint, "/bin/true") // Do nothing on execute

	// Create the new container
	container, err := dc.CreateContainer(docker.CreateContainerOptions{
		Name:   "cycore_qemu",
		Config: &config,
	})

	// Construct the untar command
	cmd := exec.Command("tar", "xf", "-", "-C", installPath)
	w, err := cmd.StdinPipe()
	if err != nil {
		glog.Fatalln("Failed to open stdin pipe for tar", err)
	}
	err = cmd.Start()
	if err != nil {
		glog.Fatalln("Failed to start tar command")
	}

	// Export the container
	exportOptions := docker.ExportImageOptions{
		Name:         "cycore_qemu",
		OutputStream: w,
	}
	go dc.ExportImage(exportOptions)

	// Wait for the end of the untar command
	err = cmd.Wait()
	if err != nil {
		glog.Fatalln("Failed to export image:", err)
	}

	// Remove the container
	removeOptions := docker.RemoveContainerOptions{
		ID:    container.ID,
		Force: true,
	}
	err = dc.RemoveContainer(removeOptions)
	if err != nil {
		glog.Errorln("Failed to remove container after export:", err)
	}
}
