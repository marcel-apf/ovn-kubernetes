#!/bin/sh

# Copy the script to /opt/cni/bin/ and run after each reboot:
#/opt/cni/bin/vdpa-helper.sh init <pf-pci-addr>

init_pf() {
	modprobe mlx5_core

	pf=$1
	[ -n "${pf}" ] || { echo "The PF argument is missing" >&2 ; exit 1; }

	pf_exists=$(lspci -s $pf | grep Ethernet | wc -l)
	[ $pf_exists -eq 1 ] || { echo "Can't find PF $pf" >&2 ; exit 1; }

	devlink dev eswitch set "pci/$pf" mode switchdev
	[ $? -eq 0 ] || { echo "Failed to switch PF $pf to switchdev mode" >&2; exit 1; }

	sleep 2

	modprobe mlx5_vdpa
	modprobe virtio_vdpa

	#manual step: ensure the pf is connected to the right ovs bridge
}

create_sf() {
	pf=$1
	[ -n "${pf}" ] || { echo "The PF argument is missing" >&2 ; exit 1; }

	name=$2
	[ -n "${name}" ] || { echo "The interface name argument is missing" >&2 ; exit 1; }

	maxsf=$(devlink port show | grep $pf | grep -o 'sfnum.*' | cut -d' ' -f2 | sort -n | tail -1)
	sfnum=$(($maxsf+1))

	output=$(devlink port add "pci/$pf" flavour pcisf pfnum 0 sfnum "$sfnum")
	[ $? -eq 0 ] || { echo "Failed to create sf $sfnum for $pf" >&2; exit 1; }

	sf=$(echo $output | cut -d':' -f1-3)
	repif=$(echo $output | grep  -o 'netdev.*' | cut -d' ' -f2)

	printf -v m1 "%02d" ${sfnum:2:3}
	printf -v m2 "%02d" ${sfnum:0:2}

	devlink port function set $sf hw_addr 00:33:33:33:$m1:$m2
	devlink port function set $sf state active

	sleep 2

	for f in $(ls /sys/bus/pci/devices/$pf/mlx5_core.sf.*/sfnum); do
		[[ $(cat $f) == $sfnum ]] && sfbus=$(echo $f | cut -d'/' -f7)
	done
	[ -n "${sfbus}" ] || { echo "Can't find $sf bus" >&2 ; exit 1; }

	vdpadev="vdpa-$name"
	vdpa dev add name $vdpadev mgmtdev auxiliary/${sfbus}

	sleep 2

	for f in $(ls /sys/class/net); do
		[ -n "$(ethtool -i $f 2>/dev/null | grep $vdpadev)" ] && virtioif=$f
	done
	[ -n "${virtioif}" ] || { echo "Can't find $sf $vdpadev $repif virtio interface" >&2 ; exit 1; }

	echo ${virtioif} $repif
}

delete_sf() {
	name=$1
	[ -n "${name}" ] || { echo "The interface name argument is missing" >&2 ; exit 1; }

	vdpadev="vdpa-$name"
	vdpa dev del $vdpadev

	output=$(devlink port show | grep $name)
	sf=$(echo $output | cut -d':' -f1-3)
	devlink port del $sf
}

case ${1} in
	init)
		init_pf "${@:2}"
                exit 0
                ;;
        create-sf)
		create_sf "${@:2}"
                exit 0
                ;;
        delete-sf)
		delete_sf "${@:2}"
                exit 0
                ;;
        *)
                echo "Wrong args!" >&2
                exit 1
                ;;
esac
