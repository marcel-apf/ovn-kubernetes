package ovn

import (
	"context"
	"fmt"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"net"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/ipallocator"
	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/libovsdbops"
)

func (oc *Controller) syncPods(pods []interface{}) {
	var allOps []ovsdb.Operation
	// get the list of logical switch ports (equivalent to pods)
	expectedLogicalPorts := make(map[string]bool)
	for _, podInterface := range pods {
		pod, ok := podInterface.(*kapi.Pod)
		if !ok {
			klog.Errorf("Spurious object in syncPods: %v", podInterface)
			continue
		}
		annotations, err := util.UnmarshalPodAnnotation(pod.Annotations)
		if util.PodScheduled(pod) && util.PodWantsNetwork(pod) && err == nil {
			logicalPort := util.GetLogicalPortName(pod.Namespace, pod.Name)
			expectedLogicalPorts[logicalPort] = true
			if err = oc.lsManager.AllocateIPs(pod.Spec.NodeName, annotations.IPs); err != nil {
				klog.Errorf("couldn't allocate IPs: %s for pod: %s on node: %s"+
					" error: %v", util.JoinIPNetIPs(annotations.IPs, " "), logicalPort,
					pod.Spec.NodeName, err)
			}
		}
	}

	// in order to minimize the number of database transactions build a map of all ports keyed by UUID
	portCache := make(map[string]nbdb.LogicalSwitchPort)
	lspList := []nbdb.LogicalSwitchPort{}
	ctx, cancel := context.WithTimeout(context.Background(), ovntypes.OVSDBTimeout)
	defer cancel()
	err := oc.nbClient.List(ctx, &lspList)
	if err != nil {
		klog.Errorf("Cannot sync pods, cannot retrieve list of logical switch ports (%+v)", err)
		return
	}
	for _, lsp := range lspList {
		portCache[lsp.UUID] = lsp
	}
	// get all the nodes from the watchFactory
	nodes, err := oc.watchFactory.GetNodes()
	if err != nil {
		klog.Errorf("Failed to get nodes: %v", err)
		return
	}
	for _, n := range nodes {
		stalePorts := []string{}
		// find the logical switch for the node
		ls, err := findLogicalSwitch(oc.nbClient, n.Name)
		if err != nil {
			klog.Errorf("Error getting logical switch for node %s: %v", n.Name, err)
			continue
		}
		for _, port := range ls.Ports {
			if portCache[port].ExternalIDs["pod"] == "true" {
				if _, ok := expectedLogicalPorts[portCache[port].Name]; !ok {
					stalePorts = append(stalePorts, port)
				}
			}
		}
		if len(stalePorts) > 0 {
			ops, err := oc.nbClient.Where(ls).Mutate(ls, model.Mutation{
				Field:   &ls.Ports,
				Mutator: ovsdb.MutateOperationDelete,
				Value:   stalePorts,
			})
			if err != nil {
				klog.Errorf("Could not generate ops to delete stale ports from logical switch %s (%+v)", n.Name, err)
				continue
			}
			allOps = append(allOps, ops...)
		}
	}
	_, err = libovsdbops.TransactAndCheck(oc.nbClient, allOps)
	if err != nil {
		klog.Errorf("Could not remove stale logicalPorts from switches (%+v)", err)
	}
}

func (oc *Controller) deleteLogicalPort(pod *kapi.Pod) {
	oc.deletePodExternalGW(pod)
	if pod.Spec.HostNetwork {
		return
	}
	if !util.PodScheduled(pod) {
		return
	}

	podDesc := pod.Namespace + "/" + pod.Name
	klog.Infof("Deleting pod: %s", podDesc)

	logicalPort := util.GetLogicalPortName(pod.Namespace, pod.Name)
	portInfo, err := oc.logicalPortCache.get(logicalPort)
	if err != nil {
		klog.Errorf(err.Error())
		// If ovnkube-master restarts, it is also possible the Pod's logical switch port
		// is not re-added into the cache. Delete logical switch port anyway.
		err = ovnNBLSPDel(oc.nbClient, logicalPort, pod.Spec.NodeName)
		if err != nil {
			klog.Errorf(err.Error())
		}

		// Even if the port is not in the cache, IPs annotated in the Pod annotation may already be allocated,
		// need to release them to avoid leakage.
		annotation, err := util.UnmarshalPodAnnotation(pod.Annotations)
		if err == nil {
			podIfAddrs := annotation.IPs
			_ = oc.lsManager.ReleaseIPs(pod.Spec.NodeName, podIfAddrs)
		}
		return
	}

	// FIXME: if any of these steps fails we need to stop and try again later...

	if err := oc.deletePodFromNamespace(pod.Namespace, portInfo); err != nil {
		klog.Errorf(err.Error())
	}

	err = ovnNBLSPDel(oc.nbClient, logicalPort, pod.Spec.NodeName)
	if err != nil {
		klog.Errorf(err.Error())
	}

	if err := oc.lsManager.ReleaseIPs(portInfo.logicalSwitch, portInfo.ips); err != nil {
		klog.Errorf(err.Error())
	}

	if config.Gateway.DisableSNATMultipleGWs {
		oc.deletePerPodGRSNAT(pod.Spec.NodeName, portInfo.ips)
	}
	podNsName := ktypes.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	oc.deleteGWRoutesForPod(podNsName, portInfo.ips)

	oc.logicalPortCache.remove(logicalPort)
}

func (oc *Controller) waitForNodeLogicalSwitch(nodeName string) (*nbdb.LogicalSwitch, error) {
	// Wait for the node logical switch to be created by the ClusterController and be present
	// in libovsdb's cache. The node switch will be created when the node's logical network infrastructure
	// is created by the node watch
	ls := &nbdb.LogicalSwitch{Name: nodeName}
	if err := wait.PollImmediate(30*time.Millisecond, 30*time.Second, func() (bool, error) {
		logicalSwitch, err := findLogicalSwitch(oc.nbClient, nodeName)
		if err != nil && err != libovsdbclient.ErrNotFound {
			return false, err
		}
		if err == nil {
			ls = logicalSwitch
			return true, nil
		}
		return false, nil
	}); err != nil {
		return nil, fmt.Errorf("timed out waiting for logical switch in libovsdb cache %q subnet: %v", nodeName, err)
	}
	return ls, nil
}

func (oc *Controller) addRoutesGatewayIP(pod *kapi.Pod, podAnnotation *util.PodAnnotation, nodeSubnets []*net.IPNet) error {
	// if there are other network attachments for the pod, then check if those network-attachment's
	// annotation has default-route key. If present, then we need to skip adding default route for
	// OVN interface
	networks, err := util.GetPodNetSelAnnotation(pod, util.NetworkAttachmentAnnotation)
	if err != nil {
		return fmt.Errorf("error while getting network attachment definition for [%s/%s]: %v",
			pod.Namespace, pod.Name, err)
	}
	otherDefaultRouteV4 := false
	otherDefaultRouteV6 := false
	for _, network := range networks {
		for _, gatewayRequest := range network.GatewayRequest {
			if utilnet.IsIPv6(gatewayRequest) {
				otherDefaultRouteV6 = true
			} else {
				otherDefaultRouteV4 = true
			}
		}
	}

	for _, podIfAddr := range podAnnotation.IPs {
		isIPv6 := utilnet.IsIPv6CIDR(podIfAddr)
		nodeSubnet, err := util.MatchIPNetFamily(isIPv6, nodeSubnets)
		if err != nil {
			return err
		}

		gatewayIPnet := util.GetNodeGatewayIfAddr(nodeSubnet)

		otherDefaultRoute := otherDefaultRouteV4
		if isIPv6 {
			otherDefaultRoute = otherDefaultRouteV6
		}
		var gatewayIP net.IP
		if otherDefaultRoute {
			for _, clusterSubnet := range config.Default.ClusterSubnets {
				if isIPv6 == utilnet.IsIPv6CIDR(clusterSubnet.CIDR) {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    clusterSubnet.CIDR,
						NextHop: gatewayIPnet.IP,
					})
				}
			}
			for _, serviceSubnet := range config.Kubernetes.ServiceCIDRs {
				if isIPv6 == utilnet.IsIPv6CIDR(serviceSubnet) {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    serviceSubnet,
						NextHop: gatewayIPnet.IP,
					})
				}
			}
		} else {
			gatewayIP = gatewayIPnet.IP
		}

		if len(config.HybridOverlay.ClusterSubnets) > 0 {
			// Add a route for each hybrid overlay subnet via the hybrid
			// overlay port on the pod's logical switch.
			nextHop := util.GetNodeHybridOverlayIfAddr(nodeSubnet).IP
			for _, clusterSubnet := range config.HybridOverlay.ClusterSubnets {
				if utilnet.IsIPv6CIDR(clusterSubnet.CIDR) == isIPv6 {
					podAnnotation.Routes = append(podAnnotation.Routes, util.PodRoute{
						Dest:    clusterSubnet.CIDR,
						NextHop: nextHop,
					})
				}
			}
		}
		if gatewayIP != nil {
			podAnnotation.Gateways = append(podAnnotation.Gateways, gatewayIP)
		}
	}
	return nil
}

func (oc *Controller) addLogicalPort(pod *kapi.Pod) (err error) {
	// If a node does node have an assigned hostsubnet don't wait for the logical switch to appear
	if oc.lsManager.IsNonHostSubnetSwitch(pod.Spec.NodeName) {
		return nil
	}

	// Keep track of how long syncs take.
	start := time.Now()
	defer func() {
		klog.Infof("[%s/%s] addLogicalPort took %v", pod.Namespace, pod.Name, time.Since(start))
	}()

	logicalSwitch := pod.Spec.NodeName
	ls, err := oc.waitForNodeLogicalSwitch(logicalSwitch)
	if err != nil {
		return err
	}

	portName := util.GetLogicalPortName(pod.Namespace, pod.Name)
	klog.Infof("[%s/%s] creating logical port for pod on switch %s", pod.Namespace, pod.Name, logicalSwitch)

	var podMac net.HardwareAddr
	var podIfAddrs []*net.IPNet
	var allOps []ovsdb.Operation
	var addresses []string
	var releaseIPs bool
	lspExist := false
	needsIP := true

	ctx, cancel := context.WithTimeout(context.Background(), ovntypes.OVSDBTimeout)
	defer cancel()
	// Check if the pod's logical switch port already exists. If it
	// does don't re-add the port to OVN as this will change its
	// UUID and and the port cache, address sets, and port groups
	// will still have the old UUID.
	getLSP := &nbdb.LogicalSwitchPort{Name: portName}
	err = oc.nbClient.Get(ctx, getLSP)
	if err != nil && err != libovsdbclient.ErrNotFound {
		return fmt.Errorf("unable to get the lsp: %s from the nbdb: %s", portName, err)
	}
	lsp := &nbdb.LogicalSwitchPort{Name: portName}
	if len(getLSP.UUID) == 0 {
		lsp.UUID = libovsdbops.BuildNamedUUID()
	} else {
		lsp.UUID = getLSP.UUID
		lspExist = true
	}

	lsp.Options = make(map[string]string)
	// Unique identifier to distinguish interfaces for recreated pods, also set by ovnkube-node
	// ovn-controller will claim the OVS interface only if external_ids:iface-id
	// matches with the Port_Binding.logical_port and external_ids:iface-id-ver matches
	// with the Port_Binding.options:iface-id-ver. This is not mandatory.
	// If Port_binding.options:iface-id-ver is not set, then OVS
	// Interface.external_ids:iface-id-ver if set is ignored.
	// Don't set iface-id-ver for already existing LSP if it wasn't set before,
	// because the corresponding OVS port may not have it set
	// (then ovn-controller won't bind the interface).
	// May happen on upgrade, because ovnkube-node doesn't update
	// existing OVS interfaces with new iface-id-ver option.
	if !lspExist || len(getLSP.Options["iface-id-ver"]) != 0 {
		lsp.Options["iface-id-ver"] = string(pod.UID)
	}
	// Bind the port to the node's chassis; prevents ping-ponging between
	// chassis if ovnkube-node isn't running correctly and hasn't cleared
	// out iface-id for an old instance of this pod, and the pod got
	// rescheduled.
	lsp.Options["requested-chassis"] = pod.Spec.NodeName

	annotation, err := util.UnmarshalPodAnnotation(pod.Annotations)

	// the IPs we allocate in this function need to be released back to the
	// IPAM pool if there is some error in any step of addLogicalPort past
	// the point the IPs were assigned via the IPAM manager.
	// this needs to be done only when releaseIPs is set to true (the case where
	// we truly have assigned podIPs in this call) AND when there is no error in
	// the rest of the functionality of addLogicalPort. It is important to use a
	// named return variable for defer to work correctly.

	defer func() {
		if releaseIPs && err != nil {
			if relErr := oc.lsManager.ReleaseIPs(logicalSwitch, podIfAddrs); relErr != nil {
				klog.Errorf("Error when releasing IPs for node: %s, err: %q",
					logicalSwitch, relErr)
			} else {
				klog.Infof("Released IPs: %s for node: %s", util.JoinIPNetIPs(podIfAddrs, " "), logicalSwitch)
			}
		}
	}()

	if err == nil {
		podMac = annotation.MAC
		podIfAddrs = annotation.IPs

		// If the pod already has annotations use the existing static
		// IP/MAC from the annotation.
		lsp.DynamicAddresses = nil

		// ensure we have reserved the IPs in the annotation
		if err = oc.lsManager.AllocateIPs(logicalSwitch, podIfAddrs); err != nil && err != ipallocator.ErrAllocated {
			return fmt.Errorf("unable to ensure IPs allocated for already annotated pod: %s, IPs: %s, error: %v",
				pod.Name, util.JoinIPNetIPs(podIfAddrs, " "), err)
		} else {
			needsIP = false
		}
	}

	if needsIP {
		// try to get the IP from existing port in OVN first
		podMac, podIfAddrs, err = oc.getPortAddresses(logicalSwitch, portName)
		if err != nil {
			return fmt.Errorf("failed to get pod addresses for pod %s on node: %s, err: %v",
				portName, logicalSwitch, err)
		}
		needsNewAllocation := false
		// ensure we have reserved the IPs found in OVN
		if len(podIfAddrs) == 0 {
			needsNewAllocation = true
		} else if err = oc.lsManager.AllocateIPs(logicalSwitch, podIfAddrs); err != nil && err != ipallocator.ErrAllocated {
			klog.Warningf("Unable to allocate IPs found on existing OVN port: %s, for pod %s on node: %s"+
				" error: %v", util.JoinIPNetIPs(podIfAddrs, " "), portName, logicalSwitch, err)

			needsNewAllocation = true
		}
		if needsNewAllocation {
			// Previous attempts to use already configured IPs failed, need to assign new
			podMac, podIfAddrs, err = oc.assignPodAddresses(logicalSwitch)
			if err != nil {
				return fmt.Errorf("failed to assign pod addresses for pod %s on node: %s, err: %v",
					portName, logicalSwitch, err)
			}
		}

		releaseIPs = true
		var networks []*types.NetworkSelectionElement

		networks, err = util.GetPodNetSelAnnotation(pod, util.DefNetworkAnnotation)
		// handle error cases separately first to ensure binding to err, otherwise the
		// defer will fail
		if err != nil {
			return fmt.Errorf("error while getting custom MAC config for port %q from "+
				"default-network's network-attachment: %v", portName, err)
		} else if networks != nil && len(networks) != 1 {
			err = fmt.Errorf("invalid network annotation size while getting custom MAC config"+
				" for port %q", portName)
			return err
		}

		if networks != nil && networks[0].MacRequest != "" {
			klog.V(5).Infof("Pod %s/%s requested custom MAC: %s", pod.Namespace, pod.Name, networks[0].MacRequest)
			podMac, err = net.ParseMAC(networks[0].MacRequest)
			if err != nil {
				return fmt.Errorf("failed to parse mac %s requested in annotation for pod %s: Error %v",
					networks[0].MacRequest, pod.Name, err)
			}
		}
		podAnnotation := util.PodAnnotation{
			IPs: podIfAddrs,
			MAC: podMac,
		}
		var nodeSubnets []*net.IPNet
		if nodeSubnets = oc.lsManager.GetSwitchSubnets(logicalSwitch); nodeSubnets == nil {
			return fmt.Errorf("cannot retrieve subnet for assigning gateway routes for pod %s, node: %s",
				pod.Name, logicalSwitch)
		}
		err = oc.addRoutesGatewayIP(pod, &podAnnotation, nodeSubnets)
		if err != nil {
			return err
		}
		var marshalledAnnotation map[string]interface{}
		marshalledAnnotation, err = util.MarshalPodAnnotation(&podAnnotation)
		if err != nil {
			return fmt.Errorf("error creating pod network annotation: %v", err)
		}

		klog.V(5).Infof("Annotation values: ip=%v ; mac=%s ; gw=%s\nAnnotation=%s",
			podIfAddrs, podMac, podAnnotation.Gateways, marshalledAnnotation)
		if err = oc.kube.SetAnnotationsOnPod(pod.Namespace, pod.Name, marshalledAnnotation); err != nil {
			return fmt.Errorf("failed to set annotation on pod %s: %v", pod.Name, err)
		}
		releaseIPs = false
	}

	// Ensure the namespace/nsInfo exists
	routingExternalGWs, routingPodGWs, err := oc.addPodToNamespace(pod.Namespace, podIfAddrs)
	if err != nil {
		return err
	}

	// if we have any external or pod Gateways, add routes
	gateways := make([]*gatewayInfo, 0)

	if len(routingExternalGWs.gws) > 0 {
		gateways = append(gateways, routingExternalGWs)
	}
	for _, gw := range routingPodGWs {
		if len(gw.gws) > 0 {
			gateways = append(gateways, gw)
		} else {
			klog.Warningf("Found routingPodGW with no gateways ip set for namespace %s", pod.Namespace)
		}
	}

	if len(gateways) > 0 {
		podNsName := ktypes.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
		err = oc.addGWRoutesForPod(gateways, podIfAddrs, podNsName, pod.Spec.NodeName)
		if err != nil {
			return err
		}
	} else if config.Gateway.DisableSNATMultipleGWs {
		// Add NAT rules to pods if disable SNAT is set and does not have
		// namespace annotations to go through external egress router
		if err = oc.addPerPodGRSNAT(pod, podIfAddrs); err != nil {
			return err
		}
	}

	// check if this pod is serving as an external GW
	err = oc.addPodExternalGW(pod)
	if err != nil {
		return fmt.Errorf("failed to handle external GW check: %v", err)
	}

	// set addresses on the port
	// LSP addresses in OVN are a single space-separated value
	addresses = []string{podMac.String()}
	for _, podIfAddr := range podIfAddrs {
		addresses[0] = addresses[0] + " " + podIfAddr.IP.String()
	}

	lsp.Addresses = addresses

	// add external ids
	lsp.ExternalIDs = map[string]string{"namespace": pod.Namespace, "pod": "true"}

	// CNI depends on the flows from port security, delay setting it until end
	lsp.PortSecurity = addresses

	if !lspExist {
		// create new logical switch port
		ops, err := oc.nbClient.Create(lsp)
		if err != nil {
			return err
		}
		allOps = append(allOps, ops...)

		//add the logical switch port to the logical switch
		ops, err = oc.nbClient.Where(ls).Mutate(ls, model.Mutation{
			Field:   &ls.Ports,
			Mutator: ovsdb.MutateOperationInsert,
			Value:   []string{lsp.UUID},
		})
		if err != nil {
			return err
		}
		allOps = append(allOps, ops...)

	} else {
		//update Existing logical switch port
		ops, err := oc.nbClient.Where(lsp).Update(lsp, &lsp.Addresses, &lsp.ExternalIDs, &lsp.Options, &lsp.PortSecurity)
		if err != nil {
			return fmt.Errorf("could not create commands to update logical switch port %s - %+v", portName, err)
		}
		allOps = append(allOps, ops...)
	}

	results, err := libovsdbops.TransactAndCheck(oc.nbClient, allOps)
	if err != nil {

		return fmt.Errorf("could not perform creation or update of logical switch port %s - %+v", portName, err)
	}

	// Add the pod's logical switch port to the port cache
	var lspUUID string
	if len(results) >= 1 && !lspExist {
		// the results may have mutltiple entries but should only be on one UUID
		lspUUID = results[0].UUID.GoUUID
	} else {
		lspUUID = lsp.UUID
	}
	portInfo := oc.logicalPortCache.add(logicalSwitch, portName, lspUUID, podMac, podIfAddrs)

	// If multicast is allowed and enabled for the namespace, add the port to the allow policy.
	// FIXME: there's a race here with the Namespace multicastUpdateNamespace() handler, but
	// it's rare and easily worked around for now.
	ns, err := oc.watchFactory.GetNamespace(pod.Namespace)
	if err != nil {
		return err
	}
	if oc.multicastSupport && isNamespaceMulticastEnabled(ns.Annotations) {
		if err := podAddAllowMulticastPolicy(oc.nbClient, pod.Namespace, portInfo); err != nil {
			return err
		}
	}
	// observe the pod creation latency metric.
	metrics.RecordPodCreated(pod)
	return nil
}

// Given a node, gets the next set of addresses (from the IPAM) for each of the node's
// subnets to assign to the new pod
func (oc *Controller) assignPodAddresses(nodeName string) (net.HardwareAddr, []*net.IPNet, error) {
	var (
		podMAC   net.HardwareAddr
		podCIDRs []*net.IPNet
		err      error
	)
	podCIDRs, err = oc.lsManager.AllocateNextIPs(nodeName)
	if err != nil {
		return nil, nil, err
	}
	if len(podCIDRs) > 0 {
		podMAC = util.IPAddrToHWAddr(podCIDRs[0].IP)
	}
	return podMAC, podCIDRs, nil
}

// Given a pod and the node on which it is scheduled, get all addresses currently assigned
// to it from the nbdb.
func (oc *Controller) getPortAddresses(nodeName, portName string) (net.HardwareAddr, []*net.IPNet, error) {
	podMac, podIPs, err := util.GetPortAddresses(portName, oc.nbClient)
	if err != nil {
		return nil, nil, err
	}

	if podMac == nil || len(podIPs) == 0 {
		return nil, nil, nil
	}

	var podIPNets []*net.IPNet

	nodeSubnets := oc.lsManager.GetSwitchSubnets(nodeName)

	for _, ip := range podIPs {
		for _, subnet := range nodeSubnets {
			if subnet.Contains(ip) {
				podIPNets = append(podIPNets,
					&net.IPNet{
						IP:   ip,
						Mask: subnet.Mask,
					})
				break
			}
		}
	}
	return podMac, podIPNets, nil
}

// ovnNBLSPDel deletes the given logical switch using the libovsdb library
func ovnNBLSPDel(client libovsdbclient.Client, logicalPort, logicalSwitch string) error {
	var allOps []ovsdb.Operation
	ls, err := findLogicalSwitch(client, logicalSwitch)
	if err != nil {
		return fmt.Errorf("could not find logicalSwitch %s - %v", logicalSwitch, err)
	}

	lsp := &nbdb.LogicalSwitchPort{Name: logicalPort}
	ctx, cancel := context.WithTimeout(context.Background(), ovntypes.OVSDBTimeout)
	defer cancel()
	err = client.Get(ctx, lsp)
	if err != nil {
		return fmt.Errorf("cannot delete logical switch port %s failed retrieving the object %v", logicalPort, err)
	}
	ops, err := client.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.Ports,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   []string{lsp.UUID},
	})
	if err != nil {
		return fmt.Errorf("cannot generate ops delete logical switch port %s: %v", logicalPort, err)
	}
	allOps = append(allOps, ops...)
	//for testing purposes the explicit delete of the logical switch port is required
	ops, err = client.Where(lsp).Delete()
	if err != nil {
		return fmt.Errorf("cannot generate ops delete logical switch port %s: %v", logicalPort, err)
	}
	allOps = append(allOps, ops...)

	_, err = libovsdbops.TransactAndCheck(client, allOps)
	if err != nil {
		return fmt.Errorf("cannot delete logical switch port %s, %v", logicalPort, err)
	}
	return nil
}

func findLogicalSwitch(nbClient libovsdbclient.Client, logicalSwitchName string) (*nbdb.LogicalSwitch, error) {
	logicalSwitches := []nbdb.LogicalSwitch{}
	ctx, cancel := context.WithTimeout(context.Background(), ovntypes.OVSDBTimeout)
	defer cancel()
	err := nbClient.WhereCache(
		func(ls *nbdb.LogicalSwitch) bool {
			return ls.Name == logicalSwitchName
		}).List(ctx, &logicalSwitches)

	if err != nil {
		return nil, fmt.Errorf("error finding logical switch %s: %v", logicalSwitchName, err)
	}

	if len(logicalSwitches) == 0 {
		return nil, libovsdbclient.ErrNotFound
	}

	if len(logicalSwitches) > 1 {
		return nil, fmt.Errorf("unexpectedly found multiple logical switches: %v", logicalSwitches)
	}

	return &logicalSwitches[0], nil
}
