#!/usr/bin/env bash

export KUBECONFIG=${HOME}/admin.conf

run_kubectl() {
  local retries=0
  local attempts=10
  while true; do
    if kubectl "$@"; then
      break
    fi

    ((retries += 1))
    if [[ "${retries}" -gt ${attempts} ]]; then
      echo "error: 'kubectl $*' did not succeed, failing"
      exit 1
    fi
    echo "info: waiting for 'kubectl $*' to succeed..."
    sleep 1
  done
}

# Some environments (Fedora32,31 on desktop), have problems when the cluster
# is deleted directly with kind `kind delete cluster --name ovn`, it restarts the host.
# The root cause is unknown, this also can not be reproduced in Ubuntu 20.04 or
# with Fedora32 Cloud, but it does not happen if we clean first the ovn-kubernetes resources.
delete() {
  timeout 5 kubectl --kubeconfig "${KUBECONFIG}" delete namespace ovn-kubernetes || true
  sleep 5
  kind delete cluster --name "${KIND_CLUSTER_NAME:-ovn}"
}

usage() {
    echo "usage: kind.sh [[[-cf |--config-file <file>] [-kt|keep-taint] [-ha|--ha-enabled]"
    echo "                 [-ho |--hybrid-enabled] [-ii|--install-ingress] [-n4|--no-ipv4]"
    echo "                 [-i6 |--ipv6] [-wk|--num-workers <num>] [-ds|--disable-snat-multiple-gws]"
    echo "                 [-dp |--disable-pkt-mtu-check]"
    echo "                 [-nf |--netflow-targets <targets>] [sf|--sflow-targets <targets>] [-if|--ipfix-targets]"
    echo "                 [-sw |--allow-system-writes] [-gm|--gateway-mode <mode>]"
    echo "                 [-nl |--node-loglevel <num>] [-ml|--master-loglevel <num>]"
    echo "                 [-dbl|--dbchecker-loglevel <num>] [-ndl|--ovn-loglevel-northd <loglevel>]"
    echo "                 [-nbl|--ovn-loglevel-nb <loglevel>] [-sbl|--ovn-loglevel-sb <loglevel>]"
    echo "                 [-cl |--ovn-loglevel-controller <loglevel>] [-dl|--ovn-loglevel-nbctld <loglevel>]"
    echo "                 [-ep |--experimental-provider <name>] |"
    echo "                 [-eb |--egress-gw-separate-bridge]"
    echo "                 [-h]]"
    echo ""
    echo "-cf  | --config-file               Name of the KIND J2 configuration file."
    echo "                                   DEFAULT: ./kind.yaml.j2"
    echo "-kt  | --keep-taint                Do not remove taint components."
    echo "                                   DEFAULT: Remove taint components."
    echo "-ha  | --ha-enabled                Enable high availability. DEFAULT: HA Disabled."
    echo "-ho  | --hybrid-enabled            Enable hybrid overlay. DEFAULT: Disabled."
    echo "-ds  | --disable-snat-multiple-gws Disable SNAT for multiple gws. DEFAULT: Disabled."
    echo "-dp  | --disable-pkt-mtu-check     Disable checking packet size greater than MTU. Default: Disabled"
    echo "-nf  | --netflow-targets           Comma delimited list of ip:port netflow collectors. DEFAULT: Disabled."
    echo "-sf  | --sflow-targets             Comma delimited list of ip:port sflow collectors. DEFAULT: Disabled."
    echo "-if  | --ipfix-targets             Comma delimited list of ip:port ipfix collectors. DEFAULT: Disabled."
    echo "-el  | --ovn-empty-lb-events       Enable empty-lb-events generation for LB without backends. DEFAULT: Disabled"
    echo "-ii  | --install-ingress           Flag to install Ingress Components."
    echo "                                   DEFAULT: Don't install ingress components."
    echo "-n4  | --no-ipv4                   Disable IPv4. DEFAULT: IPv4 Enabled."
    echo "-i6  | --ipv6                      Enable IPv6. DEFAULT: IPv6 Disabled."
    echo "-wk  | --num-workers               Number of worker nodes. DEFAULT: HA - 2 worker"
    echo "                                   nodes and no HA - 0 worker nodes."
    echo "-sw  | --allow-system-writes       Allow script to update system. Intended to allow"
    echo "                                   github CI to be updated with IPv6 settings."
    echo "                                   DEFAULT: Don't allow."
    echo "-gm  | --gateway-mode              Enable 'shared' or 'local' gateway mode."
    echo "                                   DEFAULT: shared."
    echo "-ov  | --ovn-image            	   Use the specified docker image instead of building locally. DEFAULT: local build."
    echo "-ml  | --master-loglevel           Log level for ovnkube (master), DEFAULT: 5."
    echo "-nl  | --node-loglevel             Log level for ovnkube (node), DEFAULT: 5"
    echo "-dbl | --dbchecker-loglevel        Log level for ovn-dbchecker (ovnkube-db), DEFAULT: 5."
    echo "-ndl | --ovn-loglevel-northd       Log config for ovn northd, DEFAULT: '-vconsole:info -vfile:info'."
    echo "-nbl | --ovn-loglevel-nb           Log config for northbound DB DEFAULT: '-vconsole:info -vfile:info'."
    echo "-sbl | --ovn-loglevel-sb           Log config for southboudn DB DEFAULT: '-vconsole:info -vfile:info'."
    echo "-cl  | --ovn-loglevel-controller   Log config for ovn-controller DEFAULT: '-vconsole:info'."
    echo "-dl  | --ovn-loglevel-nbctld       Log config for nbctl daemon DEFAULT: '-vconsole:info'."
    echo "-ep  | --experimental-provider     Use an experimental OCI provider such as podman, instead of docker. DEFAULT: Disabled."
    echo "-eb  | --egress-gw-separate-bridge The external gateway traffic uses a separate bridge."
    echo "--delete                      	   Delete current cluster"
    echo ""
}

parse_args() {
    while [ "$1" != "" ]; do
        case $1 in
            -cf | --config-file )               shift
                                                if test ! -f "$1"; then
                                                    echo "$1 does not  exist"
                                                    usage
                                                    exit 1
                                                fi
                                                KIND_CONFIG=$1
                                                ;;
            -ii | --install-ingress )           KIND_INSTALL_INGRESS=true
                                                ;;
            -ha | --ha-enabled )                OVN_HA=true
                                                ;;
            -me | --multicast-enabled)          OVN_MULTICAST_ENABLE=true
                                                ;;
            -ho | --hybrid-enabled )            OVN_HYBRID_OVERLAY_ENABLE=true
                                                ;;
            -ds | --disable-snat-multiple-gws ) OVN_DISABLE_SNAT_MULTIPLE_GWS=true
                                                ;;
            -dp | --disable-pkt-mtu-check )     OVN_DISABLE_PKT_MTU_CHECK=true
                                                ;;
            -ep | --experimental-provider )     shift
                                                export KIND_EXPERIMENTAL_PROVIDER=$1
                                                ;;
            -nf | --netflow-targets )           shift
                                                OVN_NETFLOW_TARGETS=$1
                                                ;;
            -sf | --sflow-targets )             shift
                                                OVN_SFLOW_TARGETS=$1
                                                ;;
            -if | --ipfix-targets )             shift
                                                OVN_IPFIX_TARGETS=$1
                                                ;;
            -el | --ovn-empty-lb-events )       OVN_EMPTY_LB_EVENTS=true
                                                ;;
            -kt | --keep-taint )                KIND_REMOVE_TAINT=false
                                                ;;
            -n4 | --no-ipv4 )                   KIND_IPV4_SUPPORT=false
                                                ;;
            -i6 | --ipv6 )                      KIND_IPV6_SUPPORT=true
                                                ;;
            -wk | --num-workers )               shift
                                                if ! [[ "$1" =~ ^[0-9]+$ ]]; then
                                                    echo "Invalid num-workers: $1"
                                                    usage
                                                    exit 1
                                                fi
                                                KIND_NUM_WORKER=$1
                                                ;;
            -sw | --allow-system-writes )       KIND_ALLOW_SYSTEM_WRITES=true
                                                ;;
            -gm | --gateway-mode )              shift
                                                if [ "$1" != "local" ] && [ "$1" != "shared" ]; then
                                                    echo "Invalid gateway mode: $1"
                                                    usage
                                                    exit 1
                                                fi
                                                OVN_GATEWAY_MODE=$1
                                                ;;
            -ov | --ovn-image )           	    shift
                                          	    OVN_IMAGE=$1
                                          	    ;;
            -ml  | --master-loglevel )          shift
                                                if ! [[ "$1" =~ ^[0-9]$ ]]; then
                                                    echo "Invalid master-loglevel: $1"
                                                    usage
                                                    exit 1
                                                fi
                                                MASTER_LOG_LEVEL=$1
                                                ;;
            -nl  | --node-loglevel )            shift
                                                if ! [[ "$1" =~ ^[0-9]$ ]]; then
                                                    echo "Invalid node-loglevel: $1"
                                                    usage
                                                    exit 1
                                                fi
                                                NODE_LOG_LEVEL=$1
                                                ;;
            -dbl | --dbchecker-loglevel )       shift
                                                if ! [[ "$1" =~ ^[0-9]$ ]]; then
                                                    echo "Invalid dbchecker-loglevel: $1"
                                                    usage
                                                    exit 1
                                                fi
                                                DBCHECKER_LOG_LEVEL=$1
                                                ;;
            -eb | --egress-gw-separate-bridge ) OVN_ENABLE_EX_GW_NETWORK_BRIDGE=true
                                                ;;
            -ndl | --ovn-loglevel-northd )      shift
                                                OVN_LOG_LEVEL_NORTHD=$1
                                                ;;
            -nbl | --ovn-loglevel-nb )          shift
                                                OVN_LOG_LEVEL_NB=$1
                                                ;;
            -sbl | --ovn-loglevel-sb )          shift
                                                OVN_LOG_LEVEL_SB=$1
                                                ;;
            -cl  | --ovn-loglevel-controller )  shift
                                                OVN_LOG_LEVEL_CONTROLLER=$1
                                                ;;
            -dl  | --ovn-loglevel-nbctld )      shift
                                                OVN_LOG_LEVEL_NBCTLD=$1
                                                ;;
            -hns | --host-network-namespace )   OVN_HOST_NETWORK_NAMESPACE=$1
                                                ;;
            --delete )                          delete
                                                exit
                                                ;;
            -h | --help )                       usage
                                                exit
                                                ;;
            * )                                 usage
                                                exit 1
        esac
        shift
    done
}

print_params() {
     echo "Using these parameters to install KIND"
     echo ""
     echo "KIND_INSTALL_INGRESS = $KIND_INSTALL_INGRESS"
     echo "OVN_HA = $OVN_HA"
     echo "KIND_CONFIG_FILE = $KIND_CONFIG"
     echo "KIND_REMOVE_TAINT = $KIND_REMOVE_TAINT"
     echo "KIND_IPV4_SUPPORT = $KIND_IPV4_SUPPORT"
     echo "KIND_IPV6_SUPPORT = $KIND_IPV6_SUPPORT"
     echo "KIND_NUM_WORKER = $KIND_NUM_WORKER"
     echo "KIND_ALLOW_SYSTEM_WRITES = $KIND_ALLOW_SYSTEM_WRITES"
     echo "KIND_EXPERIMENTAL_PROVIDER = $KIND_EXPERIMENTAL_PROVIDER"
     echo "OVN_GATEWAY_MODE = $OVN_GATEWAY_MODE"
     echo "OVN_HYBRID_OVERLAY_ENABLE = $OVN_HYBRID_OVERLAY_ENABLE"
     echo "OVN_DISABLE_SNAT_MULTIPLE_GWS = $OVN_DISABLE_SNAT_MULTIPLE_GWS"
     echo "OVN_DISABLE_PKT_MTU_CHECK = $OVN_DISABLE_PKT_MTU_CHECK"
     echo "OVN_NETFLOW_TARGETS = $OVN_NETFLOW_TARGETS"
     echo "OVN_SFLOW_TARGETS = $OVN_SFLOW_TARGETS"
     echo "OVN_IPFIX_TARGETS = $OVN_IPFIX_TARGETS"
     echo "OVN_EMPTY_LB_EVENTS = $OVN_EMPTY_LB_EVENTS"
     echo "OVN_MULTICAST_ENABLE = $OVN_MULTICAST_ENABLE"
     echo "OVN_IMAGE = $OVN_IMAGE"
     echo "MASTER_LOG_LEVEL = $MASTER_LOG_LEVEL"
     echo "NODE_LOG_LEVEL = $NODE_LOG_LEVEL"
     echo "DBCHECKER_LOG_LEVEL = $DBCHECKER_LOG_LEVEL"
     echo "OVN_LOG_LEVEL_NORTHD = $OVN_LOG_LEVEL_NORTHD"
     echo "OVN_LOG_LEVEL_NB = $OVN_LOG_LEVEL_NB"
     echo "OVN_LOG_LEVEL_SB = $OVN_LOG_LEVEL_SB"
     echo "OVN_LOG_LEVEL_CONTROLLER = $OVN_LOG_LEVEL_CONTROLLER"
     echo "OVN_LOG_LEVEL_NBCTLD = $OVN_LOG_LEVEL_NBCTLD"
     echo "OVN_HOST_NETWORK_NAMESPACE = $OVN_HOST_NETWORK_NAMESPACE"
     echo "OVN_ENABLE_EX_GW_NETWORK_BRIDGE = $OVN_ENABLE_EX_GW_NETWORK_BRIDGE"
     echo "OVN_EX_GW_NETWORK_INTERFACE = $OVN_EX_GW_NETWORK_INTERFACE"
     echo ""
}

command_exists() {
  cmd="$1"
  which ${cmd} >/dev/null 2>&1
}

check_dependencies() {
  for cmd in pip jq kind ; do
    if ! command_exists ${cmd} ; then
  	  echo "Dependency not met: Command not found '${cmd}'"
  	  exit 1
    fi
  done

  if ! command_exists docker && ! command_exists podman; then
  	  echo "Dependency not met: Neither docker nor podman found"
  	  exit 1
  fi
}

install_j2_renderer() {
  # ensure j2 renderer installed
  pip install wheel --user
  pip freeze | grep j2cli || pip install j2cli[yaml] --user
  export PATH=~/.local/bin:$PATH
}

set_default_params() {
  # Set default values
  KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-ovn}
  KIND_IMAGE=${KIND_IMAGE:-kindest/node}
  K8S_VERSION=${K8S_VERSION:-v1.20.0}
  OVN_GATEWAY_MODE=${OVN_GATEWAY_MODE:-shared}
  KIND_INSTALL_INGRESS=${KIND_INSTALL_INGRESS:-false}
  OVN_HA=${OVN_HA:-false}
  KIND_CONFIG=${KIND_CONFIG:-./kind.yaml.j2}
  KIND_REMOVE_TAINT=${KIND_REMOVE_TAINT:-true}
  KIND_IPV4_SUPPORT=${KIND_IPV4_SUPPORT:-true}
  KIND_IPV6_SUPPORT=${KIND_IPV6_SUPPORT:-false}
  OVN_HYBRID_OVERLAY_ENABLE=${OVN_HYBRID_OVERLAY_ENABLE:-false}
  OVN_DISABLE_SNAT_MULTIPLE_GWS=${OVN_DISABLE_SNAT_MULTIPLE_GWS:-false}
  OVN_DISABLE_PKT_MTU_CHECK=${OVN_DISABLE_PKT_MTU_CHECK:-false}
  OVN_EMPTY_LB_EVENTS=${OVN_EMPTY_LB_EVENTS:-false}
  OVN_MULTICAST_ENABLE=${OVN_MULTICAST_ENABLE:-false}
  KIND_ALLOW_SYSTEM_WRITES=${KIND_ALLOW_SYSTEM_WRITES:-false}
  OVN_IMAGE=${OVN_IMAGE:-local}
  MASTER_LOG_LEVEL=${MASTER_LOG_LEVEL:-5}
  NODE_LOG_LEVEL=${NODE_LOG_LEVEL:-5}
  DBCHECKER_LOG_LEVEL=${DBCHECKER_LOG_LEVEL:-5}
  OVN_LOG_LEVEL_NORTHD=${OVN_LOG_LEVEL_NORTHD:-"-vconsole:info -vfile:info"}
  OVN_LOG_LEVEL_NB=${OVN_LOG_LEVEL_NB:-"-vconsole:info -vfile:info"}
  OVN_LOG_LEVEL_SB=${OVN_LOG_LEVEL_SB:-"-vconsole:info -vfile:info"}
  OVN_LOG_LEVEL_CONTROLLER=${OVN_LOG_LEVEL_CONTROLLER:-"-vconsole:info"}
  OVN_LOG_LEVEL_NBCTLD=${OVN_LOG_LEVEL_NBCTLD:-"-vconsole:info"}
  OVN_ENABLE_EX_GW_NETWORK_BRIDGE=${OVN_ENABLE_EX_GW_NETWORK_BRIDGE:-false}
  OVN_EX_GW_NETWORK_INTERFACE=""
  if [ "$OVN_ENABLE_EX_GW_NETWORK_BRIDGE" == true ]; then
    OVN_EX_GW_NETWORK_INTERFACE="eth1"
  fi
  # Input not currently validated. Modify outside script at your own risk.
  # These are the same values defaulted to in KIND code (kind/default.go).
  # NOTE: KIND NET_CIDR_IPV6 default use a /64 but OVN have a /64 per host
  # so it needs to use a larger subnet
  #  Upstream - NET_CIDR_IPV6=fd00:10:244::/64 SVC_CIDR_IPV6=fd00:10:96::/112
  NET_CIDR_IPV4=${NET_CIDR_IPV4:-10.244.0.0/16}
  NET_SECOND_CIDR_IPV4=${NET_SECOND_CIDR_IPV4:-172.19.0.0/16}
  SVC_CIDR_IPV4=${SVC_CIDR_IPV4:-10.96.0.0/16}
  NET_CIDR_IPV6=${NET_CIDR_IPV6:-fd00:10:244::/48}
  SVC_CIDR_IPV6=${SVC_CIDR_IPV6:-fd00:10:96::/112}
  JOIN_SUBNET_IPV4=${JOIN_SUBNET_IPV4:-100.64.0.0/16}
  JOIN_SUBNET_IPV6=${JOIN_SUBNET_IPV6:-fd98::/64}
  KIND_NUM_MASTER=1
  if [ "$OVN_HA" == true ]; then
    KIND_NUM_MASTER=3
    KIND_NUM_WORKER=${KIND_NUM_WORKER:-0}
  else
    KIND_NUM_WORKER=${KIND_NUM_WORKER:-2}
  fi
  OVN_HOST_NETWORK_NAMESPACE=${OVN_HOST_NETWORK_NAMESPACE:-ovn-host-network}
  OCI_BIN=${KIND_EXPERIMENTAL_PROVIDER:-docker}
}

detect_apiserver_ip() {
  # Detect API_IP used for external communication
  #
  # You can't use an IPv6 address for the external API, docker does not support
  # IPv6 port mapping. Always use the IPv4 host address for the API Server field.
  # This will keep compatibility and people will be able to connect with kubectl
  # from outside
  #
  # ip -4 addr -> Run ip command for IPv4
  # grep -oP '(?<=inet\s)\d+(\.\d+){3}' -> Use only the lines with the
  #   IPv4 Addresses and strip off the trailing subnet mask, /xx
  # grep -v "127.0.0.1" -> Remove local host
  # head -n 1 -> Of the remaining, use first entry
  API_IP=$(ip -4 addr | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v "127.0.0.1" | head -n 1)
  if [ -z "$API_IP" ]; then
    echo "Error detecting machine IPv4 to use as API server. Default to 0.0.0.0."
    API_IP=0.0.0.0
  fi
}

detect_apiserver_url() {
  # Detect API_URL used for in-cluster communication
  #
  # Despite OVN run in pod they will only obtain the VIRTUAL apiserver address
  # and since OVN has to provide the connectivity to service
  # it can not be bootstrapped
  #
  # This is the address of the node with the control-plane
  API_URL=$(kind get kubeconfig --internal --name "${KIND_CLUSTER_NAME}" | grep server | awk '{ print $2 }')
}

check_ipv6() {
  if [ "$KIND_IPV6_SUPPORT" == true ]; then
    # Collect additional IPv6 data on test environment
    ERROR_FOUND=false
    TMPVAR=$(sysctl net.ipv6.conf.all.forwarding | awk '{print $3}')
    echo "net.ipv6.conf.all.forwarding is equal to $TMPVAR"
    if [ "$TMPVAR" != 1 ]; then
      if [ "$KIND_ALLOW_SYSTEM_WRITES" == true ]; then
	sudo sysctl -w net.ipv6.conf.all.forwarding=1
      else
	echo "RUN: 'sudo sysctl -w net.ipv6.conf.all.forwarding=1' to use IPv6."
	ERROR_FOUND=true
      fi
    fi
    TMPVAR=$(sysctl net.ipv6.conf.all.disable_ipv6 | awk '{print $3}')
    echo "net.ipv6.conf.all.disable_ipv6 is equal to $TMPVAR"
    if [ "$TMPVAR" != 0 ]; then
      if [ "$KIND_ALLOW_SYSTEM_WRITES" == true ]; then
	sudo sysctl -w net.ipv6.conf.all.disable_ipv6=0
      else
	echo "RUN: 'sudo sysctl -w net.ipv6.conf.all.disable_ipv6=0' to use IPv6."
	ERROR_FOUND=true
      fi
    fi
    if [ -f /proc/net/if_inet6 ]; then
      echo "/proc/net/if_inet6 exists so IPv6 supported in kernel."
    else
      echo "/proc/net/if_inet6 does not exists so no IPv6 support found! Compile the kernel!!"
      ERROR_FOUND=true
    fi
    if "$ERROR_FOUND"; then
      exit 2
    fi
  fi
}

set_cluster_cidr_ip_families() {
  if [ "$KIND_IPV4_SUPPORT" == true ] && [ "$KIND_IPV6_SUPPORT" == false ]; then
    IP_FAMILY=""
    NET_CIDR=$NET_CIDR_IPV4
    SVC_CIDR=$SVC_CIDR_IPV4
    echo "IPv4 Only Support: API_IP=$API_IP --net-cidr=$NET_CIDR --svc-cidr=$SVC_CIDR"
  elif [ "$KIND_IPV4_SUPPORT" == false ] && [ "$KIND_IPV6_SUPPORT" == true ]; then
    IP_FAMILY="ipv6"
    NET_CIDR=$NET_CIDR_IPV6
    SVC_CIDR=$SVC_CIDR_IPV6
    echo "IPv6 Only Support: API_IP=$API_IP --net-cidr=$NET_CIDR --svc-cidr=$SVC_CIDR"
  elif [ "$KIND_IPV4_SUPPORT" == true ] && [ "$KIND_IPV6_SUPPORT" == true ]; then
    IP_FAMILY="dual"
    NET_CIDR=$NET_CIDR_IPV4,$NET_CIDR_IPV6
    SVC_CIDR=$SVC_CIDR_IPV4,$SVC_CIDR_IPV6
    echo "Dual Stack Support: API_IP=$API_IP --net-cidr=$NET_CIDR --svc-cidr=$SVC_CIDR"
  else
    echo "Invalid setup. KIND_IPV4_SUPPORT and/or KIND_IPV6_SUPPORT must be true."
    exit 1
  fi
}

create_kind_cluster() {
  # Output of the j2 command
  KIND_CONFIG_LCL=./kind.yaml

  ovn_apiServerAddress=${API_IP} \
    ovn_ip_family=${IP_FAMILY} \
    ovn_ha=${OVN_HA} \
    net_cidr=${NET_CIDR} \
    svc_cidr=${SVC_CIDR} \
    ovn_num_master=${KIND_NUM_MASTER} \
    ovn_num_worker=${KIND_NUM_WORKER} \
    cluster_log_level=${KIND_CLUSTER_LOGLEVEL:-4} \
    j2 "${KIND_CONFIG}" -o "${KIND_CONFIG_LCL}"

  # Create KIND cluster. For additional debug, add '--verbosity <int>': 0 None .. 3 Debug
  if kind get clusters | grep ovn; then
    delete
  fi
  kind create cluster --name "${KIND_CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}" --image "${KIND_IMAGE}":"${K8S_VERSION}" --config=${KIND_CONFIG_LCL}
  cat "${KUBECONFIG}"
}

docker_disable_ipv6() {
  # Docker disables IPv6 globally inside containers except in the eth0 interface.
  # Kind enables IPv6 globally the containers ONLY for dual-stack and IPv6 deployments.
  # Ovnkube-node tries to move all global addresses from the gateway interface to the
  # bridge interface it creates. This breaks on KIND with IPv4 only deployments, because the new
  # internal bridge has IPv6 disable and can't move the IPv6 from the eth0 interface.
  # We can enable IPv6 always in the container, since the docker setup with IPv4 only
  # is not very common.
  KIND_NODES=$(kind get nodes --name "${KIND_CLUSTER_NAME}")
  for n in $KIND_NODES; do
    $OCI_BIN exec "$n" sysctl --ignore net.ipv6.conf.all.disable_ipv6=0
    $OCI_BIN exec "$n" sysctl --ignore net.ipv6.conf.all.forwarding=1
  done
}

coredns_patch() {
  dns_server="8.8.8.8"
  if [ "$KIND_IPV6_SUPPORT" == true ]; then
    dns_server="2001:4860:4860::8888"
  fi

  # Patch CoreDNS to work
  # 1. Github CI doesn´t offer IPv6 connectivity, so CoreDNS should be configured
  # to work in an offline environment:
  # https://github.com/coredns/coredns/issues/2494#issuecomment-457215452
  # 2. Github CI adds following domains to resolv.conf search field:
  # .net.
  # CoreDNS should handle those domains and answer with NXDOMAIN instead of SERVFAIL
  # otherwise pods stops trying to resolve the domain.
  # Get the current config
  original_coredns=$(kubectl get -oyaml -n=kube-system configmap/coredns)
  echo "Original CoreDNS config:"
  echo "${original_coredns}"
  # Patch it
  fixed_coredns=$(
    printf '%s' "${original_coredns}" | sed \
      -e 's/^.*kubernetes cluster\.local/& net/' \
      -e '/^.*upstream$/d' \
      -e '/^.*fallthrough.*$/d' \
      -e 's/^\(.*forward \.\).*$/\1 '"$dns_server"' {/' \
      -e '/^.*loop$/d' \
  )
  echo "Patched CoreDNS config:"
  echo "${fixed_coredns}"
  printf '%s' "${fixed_coredns}" | kubectl apply -f -
}

build_ovn_image() {
  if [ "$OVN_IMAGE" == local ]; then
    # Build ovn docker image
    pushd ../go-controller
    make
    popd

    # Build ovn kube image
    pushd ../dist/images
    # Find all built executables, but ignore the 'windows' directory if it exists
    find ../../go-controller/_output/go/bin/ -maxdepth 1 -type f -exec cp -f {} . \;
    echo "ref: $(git rev-parse  --symbolic-full-name HEAD)  commit: $(git rev-parse  HEAD)" > git_info
    $OCI_BIN build -t localhost/ovn-daemonset-f:dev -f Dockerfile.fedora .
    OVN_IMAGE=localhost/ovn-daemonset-f:dev
    popd
  fi
}

create_ovn_kube_manifests() {
  pushd ../dist/images
  ./daemonset.sh \
    --image="${OVN_IMAGE}" \
    --net-cidr="${NET_CIDR}" \
    --svc-cidr="${SVC_CIDR}" \
    --gateway-mode="${OVN_GATEWAY_MODE}" \
    --hybrid-enabled="${OVN_HYBRID_OVERLAY_ENABLE}" \
    --disable-snat-multiple-gws="${OVN_DISABLE_SNAT_MULTIPLE_GWS}" \
    --disable-pkt-mtu-check="${OVN_DISABLE_PKT_MTU_CHECK}" \
    --ovn-empty-lb-events="${OVN_EMPTY_LB_EVENTS}" \
    --multicast-enabled="${OVN_MULTICAST_ENABLE}" \
    --k8s-apiserver="${API_URL}" \
    --ovn-master-count="${KIND_NUM_MASTER}" \
    --ovn-unprivileged-mode=no \
    --master-loglevel="${MASTER_LOG_LEVEL}" \
    --node-loglevel="${NODE_LOG_LEVEL}" \
    --dbchecker-loglevel="${DBCHECKER_LOG_LEVEL}" \
    --ovn-loglevel-northd="${OVN_LOG_LEVEL_NORTHD}" \
    --ovn-loglevel-nb="${OVN_LOG_LEVEL_NB}" \
    --ovn-loglevel-sb="${OVN_LOG_LEVEL_SB}" \
    --ovn-loglevel-controller="${OVN_LOG_LEVEL_CONTROLLER}" \
    --ovn-loglevel-nbctld="${OVN_LOG_LEVEL_NBCTLD}" \
    --egress-ip-enable=true \
    --egress-firewall-enable=true \
    --v4-join-subnet="${JOIN_SUBNET_IPV4}" \
    --v6-join-subnet="${JOIN_SUBNET_IPV6}" \
    --ex-gw-network-interface="${OVN_EX_GW_NETWORK_INTERFACE}"
  popd
}

install_ovn_image() {
  if [ "$OCI_BIN" == "podman" ]; then
    # podman: cf https://github.com/kubernetes-sigs/kind/issues/2027
    rm -f /tmp/ovn-kube-f.tar
    podman save -o /tmp/ovn-kube-f.tar "${OVN_IMAGE}"
    kind load image-archive /tmp/ovn-kube-f.tar --name "${KIND_CLUSTER_NAME}"
  else
    kind load docker-image "${OVN_IMAGE}" --name "${KIND_CLUSTER_NAME}"
  fi
}

install_ovn() {
  pushd ../dist/yaml
  run_kubectl apply -f k8s.ovn.org_egressfirewalls.yaml
  run_kubectl apply -f k8s.ovn.org_egressips.yaml
  run_kubectl apply -f ovn-setup.yaml
  MASTER_NODES=$(kind get nodes --name "${KIND_CLUSTER_NAME}" | sort | head -n "${KIND_NUM_MASTER}")
  # We want OVN HA not Kubernetes HA
  # leverage the kubeadm well-known label node-role.kubernetes.io/master=
  # to choose the nodes where ovn master components will be placed
  for n in $MASTER_NODES; do
    kubectl label node "$n" k8s.ovn.org/ovnkube-db=true node-role.kubernetes.io/master="" --overwrite
    if [ "$KIND_REMOVE_TAINT" == true ]; then
      # do not error if it fails to remove the taint
      kubectl taint node "$n" node-role.kubernetes.io/master:NoSchedule- || true
    fi
  done
  if [ "$OVN_HA" == true ]; then
    run_kubectl apply -f ovnkube-db-raft.yaml
  else
    run_kubectl apply -f ovnkube-db.yaml
  fi
  run_kubectl apply -f ovs-node.yaml
  run_kubectl apply -f ovnkube-master.yaml
  run_kubectl apply -f ovnkube-node.yaml
  popd
}

install_ingress() {
  run_kubectl apply -f ingress/mandatory.yaml
  run_kubectl apply -f ingress/service-nodeport.yaml
}

kubectl_wait_pods() {
  echo "Waiting for k8s to create ovn-kubernetes pod resources..."
  local PODS_CREATED=false
  for i in {1..10}; do
    local NUM_PODS=$(kubectl -n ovn-kubernetes get pods -o json 2> /dev/null | jq '.items | length')
    if [[ "${NUM_PODS}" -ne 0 ]]; then
      echo "ovn-kubernetes pods created."
      PODS_CREATED=true
      break
    fi
    sleep 1
  done

  if [[ "${PODS_CREATED}" == false ]]; then
    echo "ovn-kubernetes pods were not created."
    exit 1
  fi
  echo "ovn-kubernetes pods created."

  # Check that everything is fine and running. IPv6 cluster seems to take a little
  # longer to come up, so extend the wait time.
  OVN_TIMEOUT=300s
  if [ "$KIND_IPV6_SUPPORT" == true ]; then
    OVN_TIMEOUT=480s
  fi
  if ! kubectl wait -n ovn-kubernetes --for=condition=ready pods --all --timeout=${OVN_TIMEOUT} ; then
    echo "some pods in OVN Kubernetes are not running"
    kubectl get pods -A -o wide || true
    exit 1
  fi
  if ! kubectl wait -n kube-system --for=condition=ready pods --all --timeout=300s ; then
    echo "some pods in the system are not running"
    kubectl get pods -A -o wide || true
    exit 1
  fi
}

docker_create_second_interface() {
  echo "adding second interfaces to nodes"

  # Create the network as dual stack, regardless of the type of the deployment. Ignore if already exists.
  docker network create --ipv6 --driver=bridge kindexgw --subnet=172.19.0.0/16 --subnet=fc00:f853:ccd:e798::/64 || true

  KIND_NODES=$(kind get nodes --name "${KIND_CLUSTER_NAME}")
  for n in $KIND_NODES; do
    docker network connect kindexgw "$n"
  done
}

sleep_until_pods_settle() {
  echo "Pods are all up, allowing things settle for 30 seconds..."
  sleep 30
}

check_dependencies
install_j2_renderer
# In order to allow providing arguments with spaces, e.g. "-vconsole:info -vfile:info" 
# the original command <parse_args $*> was replaced by <parse_args "$@">
parse_args "$@"
set_default_params
print_params
set -euxo pipefail
detect_apiserver_ip
check_ipv6
set_cluster_cidr_ip_families
create_kind_cluster
docker_disable_ipv6
if [ $OVN_ENABLE_EX_GW_NETWORK_BRIDGE == true ]; then
  docker_create_second_interface
fi
coredns_patch
build_ovn_image
detect_apiserver_url
create_ovn_kube_manifests
install_ovn_image
install_ovn
if [ "$KIND_INSTALL_INGRESS" == true ]; then
  install_ingress
fi
kubectl_wait_pods
sleep_until_pods_settle
