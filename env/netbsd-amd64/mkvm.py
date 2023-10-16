#!/usr/bin/env python
# Copyright 2016 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

import anita
import sys

arch = sys.argv[1]
release = sys.argv[2]

pkg_path = "https://cdn.netbsd.org/pub/pkgsrc/packages/NetBSD/{arch}/{release}/All".format(arch=arch, release=release)
install_packages = [
    "pkg_alternatives",
    "bash",
    "curl",
    "git-base",
    "go123",
    "doas",
    # Interactive debugging tools for users using gomote ssh
    "emacs29-nox11",
    "vim",
    "screen",
    # For https://golang.org/issue/24354
    "clang",
    "cmake",
]

commands = [
    """cat >> /etc/rc.local <<EOF
(
  export PATH=/usr/pkg/bin:/usr/pkg/sbin:\${PATH}
  export GOROOT_BOOTSTRAP=/usr/pkg/go123
  set -x
  echo 'starting bootstrapswarm'
  netstat -rn
  cat /etc/resolv.conf
  dig metadata.google.internal

  gcehost=\$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/hostname | cut -d . -f 1)
  echo "Found GCE host ${gcehost}."
  swarming=\$(curl -s -H "Metadata-Flavor: Google" http://metadata.google.internal/computeMetadata/v1/instance/attributes/swarming | cut -d . -f 1)
  swarming="\${swarming}.appspot.com"
  echo "Found Swarming host \${swarming}."
  (
    set -e
    go install -o /bootstrapswarm golang.org/x/build/cmd/bootstrapswarm@latest
    chmod +x /bootstrapswarm
    exec su -l swarming -c /bootstrapswarm -hostname \$gcehost -swarming \$swarming
  )
  echo 'giving up'
  sleep 10
  halt -p
)
EOF""",
    """cat > /etc/ifconfig.vioif0 << EOF
!dhcpcd
mtu 1460
EOF""",
    "useradd -m -p '*' swarming",
    # Prefer IPv4 over v6
    "echo ip6addrctl=YES >> /etc/rc.conf",
    "echo ip6addrctl_policy=ipv4_prefer >> /etc/rc.conf",
    "service ip6addrctl start",
    "dhcpcd -w",
    "env PKG_PATH={} pkg_add -v sqlite3".format(pkg_path),
    "env PKG_PATH={} pkg_add -v pkgin".format(pkg_path),
    "echo {} > /usr/pkg/etc/pkgin/repositories.conf".format(pkg_path),
    "pkgin update",
]
commands.extend(["pkgin -y install {}".format(x) for x in install_packages])
commands.extend([
    "pkgin clean",

    # Remove the /tmp entry, because it's mounted as tmpfs -s=ram%25 by default, which isn't enough disk space.
    """ed /etc/fstab << EOF
H
/\\/tmp/d
wq
EOF""",

    "echo sshd=yes >> /etc/rc.conf",
    "echo PermitRootLogin without-password >> /etc/ssh/sshd_config",
    "echo 'permit nopass swarming as root' > /usr/pkg/etc/doas.conf",
    "/etc/rc.d/sshd restart",
    "sync; shutdown -hp now",
])

a = anita.Anita(
    anita.URL('https://cdn.netbsd.org/pub/NetBSD/NetBSD-{}/{}/'.format(release, arch)),
    workdir="work-NetBSD-{}-{}".format(release, arch),
    disk_size="16G",
    memory_size="2G",
    persist=True)
child = a.boot()
anita.login(child)

for cmd in commands:
  anita.shell_cmd(child, cmd, 3600)

# Sometimes, the halt command times out, even though it has completed
# successfully.
try:
    a.halt()
except:
    pass
