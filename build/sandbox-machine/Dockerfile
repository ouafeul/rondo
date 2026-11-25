# A machine of rondo-sandbox: what a clone of a site's EL9 template is once cloud-init has run
# (terraform/vm/templates/cloud-init.yaml.tpl), as a container. systemd is PID 1 so that an
# installer's units start; sshd lets vm-install reach it as on a VM. Built from the repository
# root, like the other images.
# Pinned, as a site pins its template's minor release (rpmRepos el9/9.9/…): the base image,
# and the packages added from that release's frozen repositories, not the latest minor's.
ARG BASE_IMAGE=docker.io/rockylinux/rockylinux:9.9.20261112.0
FROM ${BASE_IMAGE}
# The current minor is under pub/; it moves to vault/ when the next minor ships.
ARG ROCKY_REPOS=https://dl.rockylinux.org/pub/rocky/9.9

# systemd, what terraform/vm's extra_packages installs (python3, tar; the image has
# curl-minimal), and what a template has. udev and logind have nothing to manage in a container.
# A site's machines see no Internet repository: only the component's rpmRepos.
RUN dnf -y --disablerepo='*' \
      --repofrompath=pinned-baseos,${ROCKY_REPOS}/BaseOS/x86_64/os/ \
      --repofrompath=pinned-appstream,${ROCKY_REPOS}/AppStream/x86_64/os/ \
      --setopt=pinned-baseos.gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9 \
      --setopt=pinned-appstream.gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9 \
      upgrade \
 && dnf -y --disablerepo='*' \
      --repofrompath=pinned-baseos,${ROCKY_REPOS}/BaseOS/x86_64/os/ \
      --repofrompath=pinned-appstream,${ROCKY_REPOS}/AppStream/x86_64/os/ \
      --setopt=pinned-baseos.gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9 \
      --setopt=pinned-appstream.gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9 \
      install systemd openssh-server sudo python3 tar \
 && dnf clean all \
 && systemctl enable sshd \
 && systemctl mask systemd-logind.service getty.target console-getty.service \
      systemd-udevd.service systemd-udev-trigger.service \
 && for f in /etc/yum.repos.d/*.repo; do sed -i 's/^enabled=1/enabled=0/' "$f"; done

COPY build/sandbox-machine/rondo-machine-init /usr/local/sbin/rondo-machine-init
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/usr/local/sbin/rondo-machine-init"]
