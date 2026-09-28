# M1's locally built development AMI. M2 adds the published image pipeline and
# the first-boot metadata/cloud-init path.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV container=podman

RUN apt-get update && apt-get install -y --no-install-recommends \
        systemd systemd-sysv dbus sudo \
        iproute2 iputils-ping nftables ca-certificates \
    && rm -rf /var/lib/apt/lists/*

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
