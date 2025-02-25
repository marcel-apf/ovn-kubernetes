package controller

import (
	"context"
	"fmt"
	"sync"

	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	hotypes "github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/informer"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	. "github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
)

const hoNodeCliArg string = "-no-hostsubnet-nodes=" + v1.LabelOSStable + "=windows"

func newTestNode(name, os, ovnHostSubnet, hybridHostSubnet, drMAC string) v1.Node {
	annotations := make(map[string]string)
	if ovnHostSubnet != "" {
		subnetAnnotations, err := util.CreateNodeHostSubnetAnnotation(ovntest.MustParseIPNets(ovnHostSubnet))
		Expect(err).NotTo(HaveOccurred())
		for k, v := range subnetAnnotations {
			annotations[k] = fmt.Sprintf("%s", v)
		}
	}
	if hybridHostSubnet != "" {
		annotations[hotypes.HybridOverlayNodeSubnet] = hybridHostSubnet
	}
	if drMAC != "" {
		annotations[hotypes.HybridOverlayDRMAC] = drMAC
	}
	return v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      map[string]string{v1.LabelOSStable: os},
			Annotations: annotations,
		},
	}
}

var _ = Describe("Hybrid SDN Master Operations", func() {
	var (
		app      *cli.App
		stopChan chan struct{}
		wg       *sync.WaitGroup
		fexec    *ovntest.FakeExec
	)

	BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags
		stopChan = make(chan struct{})
		wg = &sync.WaitGroup{}
		fexec = ovntest.NewFakeExec()
		err := util.SetExec(fexec)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		close(stopChan)
		wg.Wait()
	})

	const hybridOverlayClusterCIDR string = "11.1.0.0/16/24"
	It("allocates and assigns a hybrid-overlay subnet to a Windows node that doesn't have one", func() {
		app.Action = func(ctx *cli.Context) error {
			const (
				nodeName   string = "node1"
				nodeSubnet string = "11.1.0.0/24"
			)

			fakeClient := fake.NewSimpleClientset(&v1.NodeList{
				Items: []v1.Node{
					newTestNode(nodeName, "windows", "", "", ""),
				},
			})

			_, err := config.InitConfig(ctx, fexec, nil)
			Expect(err).NotTo(HaveOccurred())

			f := informers.NewSharedInformerFactory(fakeClient, informer.DefaultResyncInterval)

			dbSetup := libovsdbtest.TestSetup{}
			libovsdbOvnNBClient, err := libovsdbtest.NewNBTestHarness(dbSetup, stopChan)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			m, err := NewMaster(
				&kube.Kube{KClient: fakeClient},
				f.Core().V1().Nodes().Informer(),
				f.Core().V1().Namespaces().Informer(),
				f.Core().V1().Pods().Informer(),
				libovsdbOvnNBClient,
				informer.NewTestEventHandler,
			)
			Expect(err).NotTo(HaveOccurred())

			f.Start(stopChan)
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.Run(stopChan)
			}()
			f.WaitForCacheSync(stopChan)

			// Windows node should be allocated a subnet
			Eventually(func() (map[string]string, error) {
				updatedNode, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return updatedNode.Annotations, nil
			}, 2).Should(HaveKeyWithValue(hotypes.HybridOverlayNodeSubnet, nodeSubnet))

			Eventually(func() error {
				updatedNode, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				if err != nil {
					return err
				}
				_, err = util.ParseNodeHostSubnetAnnotation(updatedNode)
				return err
			}, 2).Should(MatchError(fmt.Sprintf("node %q has no \"k8s.ovn.org/node-subnets\" annotation", nodeName)))

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			return nil
		}

		err := app.Run([]string{
			app.Name,
			"-loglevel=5",
			"-no-hostsubnet-nodes=" + v1.LabelOSStable + "=windows",
			"-enable-hybrid-overlay",
			"-hybrid-overlay-cluster-subnets=" + hybridOverlayClusterCIDR,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("sets up and cleans up a Linux node with a OVN hostsubnet annotation", func() {
		app.Action = func(ctx *cli.Context) error {
			const (
				nodeName   string = "node1"
				nodeSubnet string = "10.1.2.0/24"
				nodeHOIP   string = "10.1.2.3"
				nodeHOMAC  string = "0a:58:0a:01:02:03"
			)

			fakeClient := fake.NewSimpleClientset(&v1.NodeList{
				Items: []v1.Node{
					newTestNode(nodeName, "linux", nodeSubnet, "", ""),
				},
			})

			_, err := config.InitConfig(ctx, fexec, nil)
			Expect(err).NotTo(HaveOccurred())

			expectedDatabaseState := []libovsdbtest.TestData{
				&nbdb.LogicalRouterPolicy{
					Priority: 1002,
					ExternalIDs: map[string]string{
						"name": "hybrid-subnet-node1",
					},
					Action:   nbdb.LogicalRouterPolicyActionReroute,
					Nexthops: []string{nodeHOIP},
					Match:    "inport == \"rtos-node1\" && ip4.dst == 11.1.0.0/16",
					UUID:     "reroute-policy-UUID",
				},
				&nbdb.LogicalRouter{
					Name:     types.OVNClusterRouter,
					Policies: []string{"reroute-policy-UUID"},
				},
				&nbdb.LogicalSwitchPort{
					Name: types.HybridOverlayPrefix + nodeName,
					UUID: types.HybridOverlayPrefix + nodeName + "-UUID",
				},
			}

			// Pre-add the HO port until the ovn-nbctl lsp-add commands are converted to libovsdb
			nodeSwitch := &nbdb.LogicalSwitch{
				Name:  nodeName,
				UUID:  nodeName + "-UUID",
				Ports: []string{types.HybridOverlayPrefix + nodeName + "-UUID"},
			}

			initialExpectedDB := append(expectedDatabaseState, nodeSwitch)

			dbSetup := libovsdbtest.TestSetup{
				NBData: initialExpectedDB,
			}
			libovsdbOvnNBClient, err := libovsdbtest.NewNBTestHarness(dbSetup, stopChan)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			f := informers.NewSharedInformerFactory(fakeClient, informer.DefaultResyncInterval)
			m, err := NewMaster(
				&kube.Kube{KClient: fakeClient},
				f.Core().V1().Nodes().Informer(),
				f.Core().V1().Namespaces().Informer(),
				f.Core().V1().Pods().Informer(),
				libovsdbOvnNBClient,
				informer.NewTestEventHandler,
			)
			Expect(err).NotTo(HaveOccurred())

			// #1 node add
			fexec.AddFakeCmdsNoOutputNoError([]string{
				// Setting the mac on the lsp
				"ovn-nbctl --timeout=15 -- " +
					"--may-exist lsp-add node1 int-node1 -- " +
					"lsp-set-addresses int-node1 " + nodeHOMAC,
			})
			// #2 comes because we set the ho dr gw mac annotation in #1
			fexec.AddFakeCmdsNoOutputNoError([]string{
				// Setting the mac on the lsp
				"ovn-nbctl --timeout=15 -- " +
					"--may-exist lsp-add node1 int-node1 -- " +
					"lsp-set-addresses int-node1 " + nodeHOMAC,
			})

			f.Start(stopChan)
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.Run(stopChan)
			}()
			f.WaitForCacheSync(stopChan)

			Eventually(func() (map[string]string, error) {
				updatedNode, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return updatedNode.Annotations, nil
			}, 2).Should(HaveKeyWithValue(hotypes.HybridOverlayDRMAC, nodeHOMAC))

			nodeSwitch.OtherConfig = map[string]string{"exclude_ips": "10.1.2.2"}

			expectedDatabaseState = append(expectedDatabaseState, nodeSwitch)

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			Eventually(libovsdbOvnNBClient).Should(libovsdbtest.HaveDataIgnoringUUIDs(expectedDatabaseState))

			// Test that deleting the node cleans up the OVN objects
			fexec.AddFakeCmdsNoOutputNoError([]string{
				"ovn-nbctl --timeout=15 -- --if-exists lsp-del int-node1",
			})

			err = fakeClient.CoreV1().Nodes().Delete(context.TODO(), nodeName, *metav1.NewDeleteOptions(0))
			Expect(err).NotTo(HaveOccurred())

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			expectedDatabaseState = []libovsdbtest.TestData{
				&nbdb.LogicalRouter{
					Name: types.OVNClusterRouter,
				},
				// This will be deleted once the nbctl commands for lsps are converted
				&nbdb.LogicalSwitchPort{
					Name: types.HybridOverlayPrefix + nodeName,
					UUID: types.HybridOverlayPrefix + nodeName + "-uuid",
				},
				nodeSwitch,
			}
			Eventually(libovsdbOvnNBClient).Should(libovsdbtest.HaveDataIgnoringUUIDs(expectedDatabaseState))
			return nil
		}

		err := app.Run([]string{
			app.Name,
			"-loglevel=5",
			"-enable-hybrid-overlay",
			"-hybrid-overlay-cluster-subnets=" + hybridOverlayClusterCIDR,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("handles a Linux node with no annotation but an existing port", func() {
		app.Action = func(ctx *cli.Context) error {
			const (
				nodeName   string = "node1"
				nodeSubnet string = "10.1.2.0/24"
				nodeHOIP   string = "10.1.2.3"
				nodeHOMAC  string = "00:00:00:52:19:d2"
			)

			fakeClient := fake.NewSimpleClientset(&v1.NodeList{
				Items: []v1.Node{
					newTestNode(nodeName, "linux", nodeSubnet, "", ""),
				},
			})

			_, err := config.InitConfig(ctx, fexec, nil)
			Expect(err).NotTo(HaveOccurred())

			dynAdd := nodeHOMAC + " " + nodeHOIP
			expectedDatabaseState := []libovsdbtest.TestData{
				&nbdb.LogicalRouterPolicy{
					Priority: 1002,
					ExternalIDs: map[string]string{
						"name": "hybrid-subnet-node1",
					},
					Action:   nbdb.LogicalRouterPolicyActionReroute,
					Nexthops: []string{nodeHOIP},
					Match:    "inport == \"rtos-node1\" && ip4.dst == 11.1.0.0/16",
					UUID:     "reroute-policy-UUID",
				},
				&nbdb.LogicalSwitchPort{
					Name:             "int-" + nodeName,
					Addresses:        []string{nodeHOMAC, nodeHOIP},
					DynamicAddresses: &dynAdd,
				},
				&nbdb.LogicalRouter{
					Name:     types.OVNClusterRouter,
					Policies: []string{"reroute-policy-UUID"},
				},
			}
			dbSetup := libovsdbtest.TestSetup{
				NBData: expectedDatabaseState,
			}
			libovsdbOvnNBClient, err := libovsdbtest.NewNBTestHarness(dbSetup, stopChan)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			f := informers.NewSharedInformerFactory(fakeClient, informer.DefaultResyncInterval)
			m, err := NewMaster(
				&kube.Kube{KClient: fakeClient},
				f.Core().V1().Nodes().Informer(),
				f.Core().V1().Namespaces().Informer(),
				f.Core().V1().Pods().Informer(),
				libovsdbOvnNBClient,
				informer.NewTestEventHandler,
			)
			Expect(err).NotTo(HaveOccurred())

			f.Start(stopChan)
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.Run(stopChan)
			}()
			f.WaitForCacheSync(stopChan)

			Eventually(func() (map[string]string, error) {
				updatedNode, err := fakeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return updatedNode.Annotations, nil
			}, 2).Should(HaveKeyWithValue(hotypes.HybridOverlayDRMAC, nodeHOMAC))

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			Eventually(libovsdbOvnNBClient).Should(libovsdbtest.HaveDataIgnoringUUIDs(expectedDatabaseState))
			return nil
		}
		err := app.Run([]string{
			app.Name,
			"-loglevel=5",
			"-enable-hybrid-overlay",
			"-hybrid-overlay-cluster-subnets=" + hybridOverlayClusterCIDR,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("cleans up a Linux node when the OVN hostsubnet annotation is removed", func() {
		app.Action = func(ctx *cli.Context) error {
			const (
				nodeName   string = "node1"
				nodeSubnet string = "10.1.2.0/24"
				nodeHOIP   string = "10.1.2.3"
				nodeHOMAC  string = "00:00:00:52:19:d2"
			)

			fakeClient := fake.NewSimpleClientset(&v1.NodeList{
				Items: []v1.Node{
					newTestNode(nodeName, "linux", nodeSubnet, "", nodeHOMAC),
				},
			})

			_, err := config.InitConfig(ctx, fexec, nil)
			Expect(err).NotTo(HaveOccurred())

			f := informers.NewSharedInformerFactory(fakeClient, informer.DefaultResyncInterval)

			dynAdd := nodeHOMAC + " " + nodeHOIP
			expectedDatabaseState := []libovsdbtest.TestData{
				&nbdb.LogicalRouterPolicy{
					Priority: 1002,
					ExternalIDs: map[string]string{
						"name": "hybrid-subnet-node1",
					},
					Action:   nbdb.LogicalRouterPolicyActionReroute,
					Nexthops: []string{nodeHOIP},
					Match:    "inport == \"rtos-node1\" && ip4.dst == 11.1.0.0/16",
					UUID:     "reroute-policy-UUID",
				},
				&nbdb.LogicalRouter{
					Name:     types.OVNClusterRouter,
					Policies: []string{"reroute-policy-UUID"},
				},
				&nbdb.LogicalSwitchPort{
					Name:             "int-" + nodeName,
					Addresses:        []string{nodeHOMAC, nodeHOIP},
					DynamicAddresses: &dynAdd,
				},
				&nbdb.LogicalSwitch{
					Name: nodeName,
					UUID: nodeName + "-UUID",
				},
			}
			dbSetup := libovsdbtest.TestSetup{
				NBData: expectedDatabaseState,
			}
			libovsdbOvnNBClient, err := libovsdbtest.NewNBTestHarness(dbSetup, stopChan)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			m, err := NewMaster(
				&kube.Kube{KClient: fakeClient},
				f.Core().V1().Nodes().Informer(),
				f.Core().V1().Namespaces().Informer(),
				f.Core().V1().Pods().Informer(),
				libovsdbOvnNBClient,
				informer.NewTestEventHandler,
			)
			Expect(err).NotTo(HaveOccurred())

			f.Start(stopChan)
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.Run(stopChan)
			}()
			f.WaitForCacheSync(stopChan)

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			Eventually(libovsdbOvnNBClient).Should(libovsdbtest.HaveDataIgnoringUUIDs(expectedDatabaseState))

			fexec.AddFakeCmdsNoOutputNoError([]string{
				"ovn-nbctl --timeout=15 -- --if-exists lsp-del int-node1",
			})
			k := &kube.Kube{KClient: fakeClient}
			updatedNode, err := k.GetNode(nodeName)
			Expect(err).NotTo(HaveOccurred())

			nodeAnnotator := kube.NewNodeAnnotator(k, updatedNode.Name)
			util.DeleteNodeHostSubnetAnnotation(nodeAnnotator)
			err = nodeAnnotator.Run()
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() (map[string]string, error) {
				updatedNode, err = k.GetNode(nodeName)
				if err != nil {
					return nil, err
				}
				return updatedNode.Annotations, nil
			}, 5).ShouldNot(HaveKey(hotypes.HybridOverlayDRMAC))

			Eventually(fexec.CalledMatchesExpected, 2).Should(BeTrue(), fexec.ErrorDesc)
			Eventually(libovsdbOvnNBClient).Should(libovsdbtest.HaveDataIgnoringUUIDs(expectedDatabaseState))
			return nil
		}

		err := app.Run([]string{
			app.Name,
			"-loglevel=5",
			"-enable-hybrid-overlay",
			"-hybrid-overlay-cluster-subnets=" + hybridOverlayClusterCIDR,
		})
		Expect(err).NotTo(HaveOccurred())
	})
})
