// +build linux

package cni

import (
	"fmt"
	"strings"

	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kexec "k8s.io/utils/exec"
)

func sfExec(cmd string, args ...string) (string, error) {
	if runner == nil {
		if err := SetExec(kexec.New()); err != nil {
			return "", err
		}
	}

	output, err := runner.Command(cmd, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("SF: failed to run '%s %s': %v\n  %q", cmd, strings.Join(args, " "), err, string(output))
	}

	return strings.TrimSuffix(string(output), "\n"), nil
}

// Setup subfunction interface in the pod
func setupSfInterface(netns ns.NetNS, containerID, ifName string, ifInfo *PodInterfaceInfo, pciAddrs string) (*current.Interface, *current.Interface, error) {
	hostIface := &current.Interface{}
	contIface := &current.Interface{}

	hostIface.Name = containerID[:15]
	outputStr, err := sfExec("/opt/cni/bin/vdpa-helper.sh", "create-sf", pciAddrs, hostIface.Name)
	if err != nil {
		return nil, nil, err
	}

	outputWords := strings.Fields(outputStr)
	vfNetdevice := outputWords[0]
	oldHostRepName := outputWords[1]

	// 5. rename the host VF representor
	if err := renameLink(oldHostRepName, hostIface.Name); err != nil {
		return nil, nil, fmt.Errorf("failed to rename %s to %s: %v", oldHostRepName, hostIface.Name, err)
	}
	link, err := util.GetNetLinkOps().LinkByName(hostIface.Name)
	if err != nil {
		return nil, nil, err
	}
	hostIface.Mac = link.Attrs().HardwareAddr.String()

	// 6. set MTU on VF representor
	if err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU); err != nil {
		return nil, nil, fmt.Errorf("failed to set MTU on %s: %v", hostIface.Name, err)
	}

	// 7. Move VF to Container namespace
	err = moveIfToNetns(vfNetdevice, netns)
	if err != nil {
		return nil, nil, err
	}

	err = netns.Do(func(hostNS ns.NetNS) error {
		contIface.Name = ifName
		err = renameLink(vfNetdevice, contIface.Name)
		if err != nil {
			return err
		}
		link, err := util.GetNetLinkOps().LinkByName(contIface.Name)
		if err != nil {
			return err
		}
		err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU)
		if err != nil {
			return err
		}
		err = util.GetNetLinkOps().LinkSetUp(link)
		if err != nil {
			return err
		}

		err = setupNetwork(link, ifInfo)
		if err != nil {
			return err
		}

		contIface.Mac = ifInfo.MAC.String()
		contIface.Sandbox = netns.Path()

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	return hostIface, contIface, nil
}

func deleteSfInterface(containerID string) error {
	ifaceName := containerID[:15]

	_, err := sfExec("/opt/cni/bin/vdpa-helper.sh", "delete-sf", ifaceName)

	return err
}
