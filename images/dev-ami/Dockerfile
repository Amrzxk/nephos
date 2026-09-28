# M1's locally built development AMI. M2 adds the published image pipeline and
# the first-boot metadata/cloud-init path.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV container=podman

RUN apt-get update && apt-get install -y --no-install-recommends \
        systemd systemd-sysv dbus sudo \
        iproute2 iputils-ping nftables ca-certificates libcap2-bin \
    && rm -rf /var/lib/apt/lists/*

# Podman's defaults exclude CAP_NET_RAW. Ubuntu's ping file capability would
# make exec fail before it can use an unprivileged ICMP socket. The runtime
# enables ping_group_range only inside each isolated instance namespace.
RUN setcap -r /usr/bin/ping \
    && test -z "$(getcap /usr/bin/ping)"

# Podman userns=auto prevents _apt from calling setgroups successfully.
RUN printf 'APT::Sandbox::User "root";\n' > /etc/apt/apt.conf.d/99-nephos-userns

# These units expect hardware that a system container deliberately lacks.
RUN systemctl mask \
        systemd-udevd.service systemd-udev-trigger.service \
        systemd-udevd-kernel.socket systemd-udevd-control.socket \
        systemd-modules-load.service systemd-timesyncd.service \
        systemd-random-seed.service systemd-firstboot.service \
        sys-kernel-debug.mount sys-kernel-config.mount \
        sys-kernel-tracing.mount dev-hugepages.mount \
    && rm -f /etc/ssh/ssh_host_*

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
